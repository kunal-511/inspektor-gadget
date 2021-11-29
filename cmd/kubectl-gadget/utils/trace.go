// Copyright 2019-2021 The Inspektor Gadget authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package utils

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	types "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"

	gadgetv1alpha1 "github.com/kinvolk/inspektor-gadget/pkg/api/v1alpha1"
	"github.com/kinvolk/inspektor-gadget/pkg/k8sutil"
)

const (
	GADGET_OPERATION = "gadget.kinvolk.io/operation"
	// We name it "global" as if one trace is created on several nodes, then each
	// copy of the trace on each node will share the same id.
	GLOBAL_TRACE_ID = "global-trace-id"
	traceTimeout    = 2 * time.Second
)

// TraceConfig is used to contain information used to manage a trace.
type TraceConfig struct {
	// GadgetName is gadget name, e.g. socket-collector.
	GadgetName string

	// Operation is the gadget operation to apply to this trace, e.g. start to
	// start the tracing.
	Operation string

	// TraceOutputMode is the trace output mode, the correct values are:
	// * "Status": The trace prints information when its status changes.
	// * "Stream": The trace prints information as events arrive.
	// * "File": The trace prints information into a file.
	// * "ExternalResource": The trace prints information an external resource,
	// e.g. a seccomp profile.
	TraceOutputMode string

	// TraceOutputState is the state in which the trace can output information.
	// For example, trace for *-collector gadget contains output while in
	// Completed state.
	// But other gadgets, like dns, can contain output only in Started state.
	TraceOutputState string

	// TraceOutput is either the name of the file when TraceOutputMode is File or
	// the name of the external resource when TraceOutputMode is ExternalResource.
	// Otherwise, its value is ignored.
	TraceOutput string

	// TraceInitialState is the state in which the trace should be after its
	// creation.
	// This field is only used by "multi-rounds gadgets" like biolatency.
	TraceInitialState string

	// CommonFlags is used to hold parameters given on the command line interface.
	CommonFlags *CommonFlags
}

func init() {
	// The Trace REST client needs to know the Trace CRD
	gadgetv1alpha1.AddToScheme(scheme.Scheme)

	// useful for randomTraceID()
	rand.Seed(time.Now().UnixNano())
}

func randomTraceID() string {
	output := make([]byte, 16)
	allowedCharacters := "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	for i := range output {
		output[i] = allowedCharacters[rand.Int31n(int32(len(allowedCharacters)))]
	}
	return string(output)
}

// If all the elements in the map have the same value, it is returned.
// Otherwise, an empty string is returned.
func getIdenticalValue(m map[string]string) string {
	value := ""
	for _, v := range m {
		if value == "" {
			value = v
		} else if value != v {
			return ""
		}
	}
	return value
}

// If there are more than one element in the map and the Error/Warning is
// the same for all the nodes, printTraceFeedback will print it only once.
func printTraceFeedback(m map[string]string, totalNodes int) {
	// Do not print `len(m)` times the same message if it's the same from all nodes
	if len(m) > 1 && len(m) == totalNodes {
		value := getIdenticalValue(m)
		if value != "" {
			fmt.Fprintf(os.Stderr, "Failed to run the gadget on all nodes: %s\n", value)
			return
		}
	}

	for node, msg := range m {
		fmt.Fprintf(os.Stderr, "Failed to run the gadget on node %q: %s\n", node, msg)
	}
}

func deleteTraces(traceRestClient *restclient.RESTClient, traceID string) {
	var listTracesOptions = metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", GLOBAL_TRACE_ID, traceID),
	}
	err := traceRestClient.
		Delete().
		Namespace("gadget").
		Resource("traces").
		VersionedParams(&listTracesOptions, scheme.ParameterCodec).
		Do(context.TODO()).
		Error()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error deleting traces: %q", err)
	}
}

// getRestClient returns the RESTClient associated with kubeRestConfig() for
// APIPath /apis and GroupVersion &gadgetv1alpha1.GroupVersion.
func getRestClient() (*restclient.RESTClient, error) {
	restConfig, err := kubeRestConfig()
	if err != nil {
		return nil, fmt.Errorf("Error while getting rest config: %w", err)
	}

	traceConfig := *restConfig
	traceConfig.ContentConfig.GroupVersion = &gadgetv1alpha1.GroupVersion
	traceConfig.APIPath = "/apis"
	traceConfig.NegotiatedSerializer = serializer.NewCodecFactory(scheme.Scheme)
	traceConfig.UserAgent = restclient.DefaultKubernetesUserAgent()

	traceRestClient, err := restclient.UnversionedRESTClientFor(&traceConfig)
	if err != nil {
		return nil, fmt.Errorf("Error setting up trace REST client: %w", err)
	}

	return traceRestClient, nil
}

// createTraces creates a trace using Kubernetes REST API.
// Note that, this function will create the trace on all existing node if
// trace.Spec.Node is empty.
func createTraces(trace *gadgetv1alpha1.Trace) error {
	client, err := k8sutil.NewClientsetFromConfigFlags(KubernetesConfigFlags)
	if err != nil {
		return fmt.Errorf("Error setting up Kubernetes client: %w", err)
	}

	nodes, err := client.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("Error listing nodes: %w", err)
	}

	traceRestClient, err := getRestClient()
	if err != nil {
		return fmt.Errorf("Error setting up trace REST client: %w", err)
	}

	traceNode := trace.Spec.Node
	for _, node := range nodes.Items {
		if traceNode != "" && node.Name != traceNode {
			continue
		}
		// If no particular node was given, we need to apply this trace on all
		// available nodes.
		if traceNode == "" {
			trace.Spec.Node = node.Name
		}

		err = traceRestClient.
			Post().
			Namespace(trace.ObjectMeta.Namespace).
			Resource("traces").
			Body(trace).
			Do(context.TODO()).
			Error()
		if err != nil {
			traceID, present := trace.ObjectMeta.Labels[GLOBAL_TRACE_ID]
			if present {
				// Clean before exiting!
				deleteTraces(traceRestClient, traceID)
			}

			return fmt.Errorf("Error creating trace on node %q: %w", node.Name, err)
		}
	}

	return nil
}

// updateTraceOperation updates operation for an already existing trace using
// Kubernetes REST API.
func updateTraceOperation(trace *gadgetv1alpha1.Trace, operation string) error {
	traceRestClient, err := getRestClient()
	if err != nil {
		return err
	}

	// This trace will be used as JSON merge patch to update GADGET_OPERATION,
	// see:
	// https://datatracker.ietf.org/doc/html/rfc6902
	// https://datatracker.ietf.org/doc/html/rfc7386
	type Annotations map[string]string
	type ObjectMeta struct {
		Annotations Annotations `json:"annotations"`
	}
	type JSONMergePatch struct {
		ObjectMeta ObjectMeta `json:"metadata"`
	}
	patch := JSONMergePatch{
		ObjectMeta: ObjectMeta{
			Annotations{
				GADGET_OPERATION: operation,
			},
		},
	}

	patchBytes, err := json.Marshal(patch)
	if err != nil {
		return fmt.Errorf("Error marshalling the operation annotations: %w", err)
	}

	return traceRestClient.
		Patch(types.MergePatchType).
		Namespace(trace.ObjectMeta.Namespace).
		Resource("traces").
		Name(trace.ObjectMeta.Name).
		Body(patchBytes).
		Do(context.TODO()).
		Error()
}

// CreateTrace initializes a trace object with its field according to the given
// parameter.
// The trace is then posted to the RESTClient which returns an error if
// something wrong occurred.
// A unique trace identifier is returned, this identifier will be used as other
// function parameter.
// A trace obtained with this function must be deleted calling DeleteTrace.
// Note that, if config.TraceInitialState is not empty, this function will
// succeed only if the trace was created and goes into the requested state.
func CreateTrace(config *TraceConfig) (string, error) {
	traceID := randomTraceID()

	var filter *gadgetv1alpha1.ContainerFilter

	// Keep Filter field empty if it is not really used
	if config.CommonFlags.Namespace != "" || config.CommonFlags.Podname != "" ||
		config.CommonFlags.Containername != "" || len(config.CommonFlags.Labels) > 0 {
		filter = &gadgetv1alpha1.ContainerFilter{
			Namespace:     config.CommonFlags.Namespace,
			Podname:       config.CommonFlags.Podname,
			ContainerName: config.CommonFlags.Containername,
			Labels:        config.CommonFlags.Labels,
		}
	}

	trace := &gadgetv1alpha1.Trace{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: config.GadgetName + "-",
			Namespace:    "gadget",
			Annotations: map[string]string{
				GADGET_OPERATION: config.Operation,
			},
			Labels: map[string]string{
				GLOBAL_TRACE_ID: traceID,
				// Add all this information here to be able to find the trace thanks
				// to them when calling getTraceListFromParameters().
				"gadgetName":    config.GadgetName,
				"nodeName":      config.CommonFlags.Node,
				"namespace":     config.CommonFlags.Namespace,
				"podName":       config.CommonFlags.Podname,
				"containerName": config.CommonFlags.Containername,
				"outputMode":    config.TraceOutputMode,
				// We will not add config.TraceOutput as label because it can contain
				// "/" which is forbidden in labels.
			},
		},
		Spec: gadgetv1alpha1.TraceSpec{
			Node:       config.CommonFlags.Node,
			Gadget:     config.GadgetName,
			Filter:     filter,
			RunMode:    "Manual",
			OutputMode: config.TraceOutputMode,
			Output:     config.TraceOutput,
		},
	}

	err := createTraces(trace)
	if err != nil {
		return "", err
	}

	if config.TraceInitialState != "" {
		// Once the traces are created, we wait for them to be in
		// config.TraceInitialState state, so they are ready to be used by the user.
		_, err = waitForTraceState(traceID, config.TraceInitialState)
		if err != nil {
			deleteError := DeleteTrace(traceID)

			if deleteError != nil {
				fmt.Fprintf(os.Stderr, "%v\n", err)
			}

			return "", err
		}
	}

	return traceID, nil
}

// getTraceListFromOptions returns a list of traces corresponding to the given
// options.
func getTraceListFromOptions(listTracesOptions metav1.ListOptions) (gadgetv1alpha1.TraceList, error) {
	traceRestClient, err := getRestClient()
	if err != nil {
		return gadgetv1alpha1.TraceList{}, err
	}

	var traces gadgetv1alpha1.TraceList

	err = traceRestClient.
		Get().
		Namespace("gadget").
		Resource("traces").
		VersionedParams(&listTracesOptions, scheme.ParameterCodec).
		Do(context.TODO()).
		Into(&traces)
	if err != nil {
		return traces, err
	}

	return traces, nil
}

// getTraceListFromID returns an array of pointers to gadgetv1alpha1.Trace
// corresponding to the given traceID.
// If no trace corresponds to this ID, error is set.
func getTraceListFromID(traceID string) (gadgetv1alpha1.TraceList, error) {
	var listTracesOptions = metav1.ListOptions{
		LabelSelector: fmt.Sprintf("%s=%s", GLOBAL_TRACE_ID, traceID),
	}

	traces, err := getTraceListFromOptions(listTracesOptions)
	if err != nil {
		return traces, fmt.Errorf("Error getting traces from traceID %q: %w", traceID, err)
	}

	if len(traces.Items) == 0 {
		return traces, fmt.Errorf("No traces found for traceID %q!", traceID)
	}

	return traces, nil
}

// SetTraceOperation sets the operation of an existing trace.
// If trace does not exist an error is returned.
func SetTraceOperation(traceID string, operation string) error {
	traces, err := getTraceListFromID(traceID)
	if err != nil {
		return err
	}

	for _, trace := range traces.Items {
		localError := updateTraceOperation(&trace, operation)
		if localError != nil {
			err = fmt.Errorf("%w\nError updating trace operation for %s: %v", err, traceID, localError)
		}
	}

	return err
}

// waitForTraceState loops over all trace whom ID is given as parameter
// waiting until they are in the expected state.
func waitForTraceState(traceID string, expectedState string) (gadgetv1alpha1.TraceList, error) {
	start := time.Now()

RetryLoop:
	for {
		successNodeCount := 0
		timeout := time.Since(start) > traceTimeout
		nodeErrors := make(map[string]string)
		nodeWarnings := make(map[string]string)

		traces, err := getTraceListFromID(traceID)
		if err != nil {
			return gadgetv1alpha1.TraceList{}, err
		}

		for _, i := range traces.Items {
			if i.Status.OperationError != "" {
				nodeErrors[i.Spec.Node] = i.Status.OperationError
			} else if i.Status.OperationWarning != "" {
				nodeWarnings[i.Spec.Node] = i.Status.OperationWarning
				// TODO(francis) This code will not work if the trace is already in the
				// expected state, for example if we decide to generate it twice.
				// We need to add a cookie (and check it) to be sure the trace is ready.
			} else if i.Status.State == expectedState {
				successNodeCount++
			} else {
				// Consider Trace as timed out if it neither moved the state forward
				// nor notified of an error or warning within the time window.
				if timeout {
					nodeErrors[i.Spec.Node] = fmt.Sprintf("No results received from trace within %v", traceTimeout)
					continue
				}

				time.Sleep(100 * time.Millisecond)
				continue RetryLoop
			}
		}

		// Print errors even if some nodes succeeded.
		defer printTraceFeedback(nodeErrors, len(traces.Items))

		// Don't print warnings if at least one node succeeded. This avoids showing
		// warnings together with the actual output generated by other nodes.
		if successNodeCount == 0 {
			printTraceFeedback(nodeWarnings, len(traces.Items))

			return gadgetv1alpha1.TraceList{}, errors.New("Failed to run the gadget on all nodes: None of them succeeded")
		}

		return traces, nil
	}
}

var sigIntReceivedNumber = 0

// sigHandler installs a handler for all signals which cause termination as
// their default behavior.
// On reception of this signal, the given trace will be deleted.
// This function fixes trace not being deleted when calling:
// kubectl gadget process-collector -A | head -n0
func sigHandler(traceID *string) {
	c := make(chan os.Signal)
	signal.Notify(c, syscall.SIGHUP, syscall.SIGINT, syscall.SIGQUIT, syscall.SIGILL, syscall.SIGABRT, syscall.SIGFPE, syscall.SIGKILL, syscall.SIGSEGV, syscall.SIGPIPE, syscall.SIGALRM, syscall.SIGTERM, syscall.SIGBUS, syscall.SIGTRAP)
	go func() {
		sig := <-c

		// This code is here in case DeleteTrace() hangs.
		// In this case, we install again this handler and if SIGINT is received
		// another time (thus getting it twice) we exit the whole program without
		// trying to delete the trace.
		if sig == syscall.SIGINT {
			sigIntReceivedNumber++

			if sigIntReceivedNumber > 1 {
				os.Exit(1)
			}

			sigHandler(traceID)
		}

		if *traceID != "" {
			DeleteTrace(*traceID)
		}

		os.Exit(1)
	}()
}

// PrintTraceOutputFromStream is used to print trace output using generic
// printing function.
// This function is must be used by trace which has TraceOutputMode set to
// Stream.
func PrintTraceOutputFromStream(traceID string, expectedState string, params *CommonFlags, transformLine func(string) string) error {
	traces, err := waitForTraceState(traceID, expectedState)
	if err != nil {
		return err
	}

	return genericStreamsDisplay(params, &traces, transformLine)
}

// PrintTraceOutputFromStatus is used to print trace output using function
// pointer provided by caller.
// It will parse trace.Spec.Output and print it calling the function pointer.
func PrintTraceOutputFromStatus(traceID string, expectedState string, customResultsDisplay func(results []gadgetv1alpha1.Trace) error) error {
	traces, err := waitForTraceState(traceID, expectedState)
	if err != nil {
		return err
	}

	return customResultsDisplay(traces.Items)
}

// DeleteTrace deletes the traces for the given trace ID using RESTClient.
func DeleteTrace(traceID string) error {
	traceRestClient, err := getRestClient()
	if err != nil {
		return err
	}

	deleteTraces(traceRestClient, traceID)

	return nil
}

// labelsFromFilter creates a string containing labels value from the given
// labelFilter.
func labelsFromFilter(filter map[string]string) string {
	labels := ""
	separator := ""

	// Loop on all fields of labelFilter.
	for labelName, labelValue := range filter {
		// If this field has no value, just skip it.
		if labelValue == "" {
			continue
		}

		// Concatenate the label to existing one.
		labels = fmt.Sprintf("%s%s%s=%v", labels, separator, labelName, labelValue)
		separator = ","
	}

	return labels
}

// getTraceListFromParameters returns traces associated with the given config.
func getTraceListFromParameters(config *TraceConfig) ([]gadgetv1alpha1.Trace, error) {
	filter := map[string]string{
		"gadgetName":    config.GadgetName,
		"nodeName":      config.CommonFlags.Node,
		"namespace":     config.CommonFlags.Namespace,
		"podName":       config.CommonFlags.Podname,
		"containerName": config.CommonFlags.Containername,
		"outputMode":    config.TraceOutputMode,
	}

	var listTracesOptions = metav1.ListOptions{
		LabelSelector: labelsFromFilter(filter),
	}

	traces, err := getTraceListFromOptions(listTracesOptions)
	if err != nil {
		return []gadgetv1alpha1.Trace{}, err
	}

	return traces.Items, nil
}

// PrintAllTraces prints all traces corresponding to the given config.CommonFlags.
func PrintAllTraces(config *TraceConfig) error {
	traces, err := getTraceListFromParameters(config)
	if err != nil {
		return err
	}

	type printingInformation struct {
		namespace     string
		nodes         []string
		podname       string
		containerName string
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 4, ' ', 0)

	fmt.Fprintln(w, "NAMESPACE\tNODE(S)\tPOD\tCONTAINER\tTRACEID")

	printingMap := map[string]*printingInformation{}

	for _, trace := range traces {
		id, present := trace.ObjectMeta.Labels[GLOBAL_TRACE_ID]
		if !present {
			continue
		}

		node := trace.Spec.Node

		_, present = printingMap[id]
		if present {
			if node == "" {
				continue
			}

			// If an entry with this traceID already exists, we just update the node
			// name by concatenating it to the string.
			printingMap[id].nodes = append(printingMap[id].nodes, node)
		} else {
			// Otherwise, we simply create a new entry.
			if filter := trace.Spec.Filter; filter != nil {
				printingMap[id] = &printingInformation{
					namespace:     filter.Namespace,
					nodes:         []string{node},
					podname:       filter.Podname,
					containerName: filter.ContainerName,
				}
			} else {
				printingMap[id] = &printingInformation{
					nodes: []string{node},
				}
			}
		}
	}

	for id, info := range printingMap {
		sort.Strings(info.nodes)
		fmt.Fprintf(w, "%v\t%v\t%v\t%v\t%v\n", info.namespace, strings.Join(info.nodes, ","), info.podname, info.containerName, id)
	}

	w.Flush()

	return nil
}

// RunTraceAndPrintStream creates a trace, prints its output and deletes
// it.
// It equals calling separately CreateTrace(), then PrintTraceOutputFromStream()
// and DeleteTrace().
// This function is thought to be used with "one-run" gadget, i.e. gadget
// which runs a trace when it is created.
func RunTraceAndPrintStream(config *TraceConfig, transformLine func(string) string) error {
	var traceID string

	sigHandler(&traceID)

	if config.TraceOutputMode != "Stream" {
		return errors.New("TraceOutputMode must be Stream. Otherwise, call RunTraceAndPrintStatusOutput!")
	}

	traceID, err := CreateTrace(config)
	if err != nil {
		return fmt.Errorf("error creating trace: %w", err)
	}

	defer DeleteTrace(traceID)

	return PrintTraceOutputFromStream(traceID, config.TraceOutputState, config.CommonFlags, transformLine)
}

// RunTraceAndPrintStatusOutput creates a trace, prints its output and deletes
// it.
// It equals calling separately CreateTrace(), then PrintTraceOutputFromStatus()
// and DeleteTrace().
// This function is thought to be used with "one-run" gadget, i.e. gadget
// which runs a trace when it is created.
func RunTraceAndPrintStatusOutput(config *TraceConfig, customResultsDisplay func(results []gadgetv1alpha1.Trace) error) error {
	var traceID string

	sigHandler(&traceID)

	if config.TraceOutputMode == "Stream" {
		return errors.New("TraceOutputMode must not be Stream. Otherwise, call RunTraceAndPrintStream!")
	}

	traceID, err := CreateTrace(config)
	if err != nil {
		return fmt.Errorf("error creating trace: %w", err)
	}

	defer DeleteTrace(traceID)

	return PrintTraceOutputFromStatus(traceID, config.TraceOutputState, customResultsDisplay)
}

func genericStreamsDisplay(
	params *CommonFlags,
	results *gadgetv1alpha1.TraceList,
	transformLine func(string) string,
) error {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	completion := make(chan string)

	client, err := k8sutil.NewClientsetFromConfigFlags(KubernetesConfigFlags)
	if err != nil {
		return fmt.Errorf("Error setting up Kubernetes client: %w", err)
	}

	callback := func(line string) string {
		if params.OutputMode == OutputModeJson {
			return line
		}
		return transformLine(line)
	}

	verbose := false
	// verbose only when not json is used
	if params.Verbose && params.OutputMode != OutputModeJson {
		verbose = true
	}

	config := &PostProcessConfig{
		Flows:     len(results.Items),
		OutStream: os.Stdout,
		ErrStream: os.Stderr,
		Transform: callback,
		Verbose:   verbose,
	}

	postProcess := NewPostProcess(config)

	streamCount := int32(0)
	for index, i := range results.Items {
		if params.Node != "" && i.Spec.Node != params.Node {
			continue
		}
		atomic.AddInt32(&streamCount, 1)
		go func(nodeName, namespace, name string, index int) {
			cmd := fmt.Sprintf("exec gadgettracermanager -call receive-stream -tracerid trace_%s_%s",
				namespace, name)
			err := ExecPod(client, nodeName, cmd,
				postProcess.OutStreams[index], postProcess.ErrStreams[index])
			if err == nil {
				completion <- fmt.Sprintf("Trace completed on node %s\n", nodeName)
			} else {
				completion <- fmt.Sprintf("Error running command on node %s: %v\n", nodeName, err)
			}
		}(i.Spec.Node, i.ObjectMeta.Namespace, i.ObjectMeta.Name, index)
	}

	for {
		select {
		case <-sigs:
			if params.OutputMode != OutputModeJson {
				fmt.Println("\nTerminating...")
			}
			return nil
		case msg := <-completion:
			fmt.Printf("%s", msg)
			if atomic.AddInt32(&streamCount, -1) == 0 {
				return nil
			}
		}
	}
}

// DeleteTraceByGadgetName removes all traces with this gadget name
func DeleteTracesByGadgetName(gadget string) error {
	traceRestClient, err := getRestClient()
	if err != nil {
		return err
	}

	var listTracesOptions = metav1.ListOptions{
		LabelSelector: fmt.Sprintf("gadgetName=%s", gadget),
	}
	return traceRestClient.
		Delete().
		Namespace("gadget").
		Resource("traces").
		VersionedParams(&listTracesOptions, scheme.ParameterCodec).
		Do(context.TODO()).
		Error()
}

func ListTracesByGadgetName(gadget string) ([]gadgetv1alpha1.Trace, error) {
	var listTracesOptions = metav1.ListOptions{
		LabelSelector: fmt.Sprintf("gadgetName=%s", gadget),
	}

	traces, err := getTraceListFromOptions(listTracesOptions)
	if err != nil {
		return nil, fmt.Errorf("Error getting traces by gadget name %w", err)
	}

	return traces.Items, nil
}
