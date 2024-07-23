// Code generated by bpf2go; DO NOT EDIT.
//go:build 386 || amd64

package tracer

import (
	"bytes"
	_ "embed"
	"fmt"
	"io"

	"github.com/cilium/ebpf"
)

type capabilitiesArgsT struct {
	CurrentUserns uint64
	TargetUserns  uint64
	CapEffective  uint64
	Cap           int32
	CapOpt        int32
}

type capabilitiesCapEvent struct {
	Mntnsid       uint64
	CurrentUserns uint64
	TargetUserns  uint64
	CapEffective  uint64
	Timestamp     uint64
	Pid           uint32
	Cap           int32
	Tgid          uint32
	Uid           uint32
	Gid           uint32
	Ret           int32
	Audit         int32
	Insetid       int32
	Syscall       uint64
	Task          [16]uint8
}

type capabilitiesUniqueKey struct {
	Cap     int32
	_       [4]byte
	MntnsId uint64
}

// loadCapabilities returns the embedded CollectionSpec for capabilities.
func loadCapabilities() (*ebpf.CollectionSpec, error) {
	reader := bytes.NewReader(_CapabilitiesBytes)
	spec, err := ebpf.LoadCollectionSpecFromReader(reader)
	if err != nil {
		return nil, fmt.Errorf("can't load capabilities: %w", err)
	}

	return spec, err
}

// loadCapabilitiesObjects loads capabilities and converts it into a struct.
//
// The following types are suitable as obj argument:
//
//	*capabilitiesObjects
//	*capabilitiesPrograms
//	*capabilitiesMaps
//
// See ebpf.CollectionSpec.LoadAndAssign documentation for details.
func loadCapabilitiesObjects(obj interface{}, opts *ebpf.CollectionOptions) error {
	spec, err := loadCapabilities()
	if err != nil {
		return err
	}

	return spec.LoadAndAssign(obj, opts)
}

// capabilitiesSpecs contains maps and programs before they are loaded into the kernel.
//
// It can be passed ebpf.CollectionSpec.Assign.
type capabilitiesSpecs struct {
	capabilitiesProgramSpecs
	capabilitiesMapSpecs
}

// capabilitiesSpecs contains programs before they are loaded into the kernel.
//
// It can be passed ebpf.CollectionSpec.Assign.
type capabilitiesProgramSpecs struct {
	IgCapSchedExec *ebpf.ProgramSpec `ebpf:"ig_cap_sched_exec"`
	IgCapSchedExit *ebpf.ProgramSpec `ebpf:"ig_cap_sched_exit"`
	IgCapSysEnter  *ebpf.ProgramSpec `ebpf:"ig_cap_sys_enter"`
	IgCapSysExit   *ebpf.ProgramSpec `ebpf:"ig_cap_sys_exit"`
	IgTraceCapE    *ebpf.ProgramSpec `ebpf:"ig_trace_cap_e"`
	IgTraceCapX    *ebpf.ProgramSpec `ebpf:"ig_trace_cap_x"`
}

// capabilitiesMapSpecs contains maps before they are loaded into the kernel.
//
// It can be passed ebpf.CollectionSpec.Assign.
type capabilitiesMapSpecs struct {
	CurrentSyscall       *ebpf.MapSpec `ebpf:"current_syscall"`
	Events               *ebpf.MapSpec `ebpf:"events"`
	GadgetMntnsFilterMap *ebpf.MapSpec `ebpf:"gadget_mntns_filter_map"`
	Seen                 *ebpf.MapSpec `ebpf:"seen"`
	Start                *ebpf.MapSpec `ebpf:"start"`
}

// capabilitiesObjects contains all objects after they have been loaded into the kernel.
//
// It can be passed to loadCapabilitiesObjects or ebpf.CollectionSpec.LoadAndAssign.
type capabilitiesObjects struct {
	capabilitiesPrograms
	capabilitiesMaps
}

func (o *capabilitiesObjects) Close() error {
	return _CapabilitiesClose(
		&o.capabilitiesPrograms,
		&o.capabilitiesMaps,
	)
}

// capabilitiesMaps contains all maps after they have been loaded into the kernel.
//
// It can be passed to loadCapabilitiesObjects or ebpf.CollectionSpec.LoadAndAssign.
type capabilitiesMaps struct {
	CurrentSyscall       *ebpf.Map `ebpf:"current_syscall"`
	Events               *ebpf.Map `ebpf:"events"`
	GadgetMntnsFilterMap *ebpf.Map `ebpf:"gadget_mntns_filter_map"`
	Seen                 *ebpf.Map `ebpf:"seen"`
	Start                *ebpf.Map `ebpf:"start"`
}

func (m *capabilitiesMaps) Close() error {
	return _CapabilitiesClose(
		m.CurrentSyscall,
		m.Events,
		m.GadgetMntnsFilterMap,
		m.Seen,
		m.Start,
	)
}

// capabilitiesPrograms contains all programs after they have been loaded into the kernel.
//
// It can be passed to loadCapabilitiesObjects or ebpf.CollectionSpec.LoadAndAssign.
type capabilitiesPrograms struct {
	IgCapSchedExec *ebpf.Program `ebpf:"ig_cap_sched_exec"`
	IgCapSchedExit *ebpf.Program `ebpf:"ig_cap_sched_exit"`
	IgCapSysEnter  *ebpf.Program `ebpf:"ig_cap_sys_enter"`
	IgCapSysExit   *ebpf.Program `ebpf:"ig_cap_sys_exit"`
	IgTraceCapE    *ebpf.Program `ebpf:"ig_trace_cap_e"`
	IgTraceCapX    *ebpf.Program `ebpf:"ig_trace_cap_x"`
}

func (p *capabilitiesPrograms) Close() error {
	return _CapabilitiesClose(
		p.IgCapSchedExec,
		p.IgCapSchedExit,
		p.IgCapSysEnter,
		p.IgCapSysExit,
		p.IgTraceCapE,
		p.IgTraceCapX,
	)
}

func _CapabilitiesClose(closers ...io.Closer) error {
	for _, closer := range closers {
		if err := closer.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Do not access this directly.
//
//go:embed capabilities_x86_bpfel.o
var _CapabilitiesBytes []byte
