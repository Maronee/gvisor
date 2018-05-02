// Copyright 2018 Google Inc.
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

package kvm

import (
	"fmt"
	"sync/atomic"
	"syscall"
	"unsafe"

	"gvisor.googlesource.com/gvisor/pkg/abi/linux"
	"gvisor.googlesource.com/gvisor/pkg/sentry/platform/ring0/pagetables"
)

//go:linkname entersyscall runtime.entersyscall
func entersyscall()

//go:linkname exitsyscall runtime.exitsyscall
func exitsyscall()

// TranslateToVirtual implements pagetables.Translater.TranslateToPhysical.
func (m *machine) TranslateToPhysical(ptes *pagetables.PTEs) uintptr {
	// The length doesn't matter because all these translations require
	// only a single page, which is guaranteed to be satisfied.
	physical, _, ok := TranslateToPhysical(uintptr(unsafe.Pointer(ptes)))
	if !ok {
		panic("unable to translate pagetables.Node to physical address")
	}
	return physical
}

// mapRunData maps the vCPU run data.
func mapRunData(fd int) (*runData, error) {
	r, _, errno := syscall.RawSyscall6(
		syscall.SYS_MMAP,
		0,
		uintptr(runDataSize),
		syscall.PROT_READ|syscall.PROT_WRITE,
		syscall.MAP_SHARED,
		uintptr(fd),
		0)
	if errno != 0 {
		return nil, fmt.Errorf("error mapping runData: %v", errno)
	}
	return (*runData)(unsafe.Pointer(r)), nil
}

// unmapRunData unmaps the vCPU run data.
func unmapRunData(r *runData) error {
	if _, _, errno := syscall.RawSyscall(
		syscall.SYS_MUNMAP,
		uintptr(unsafe.Pointer(r)),
		uintptr(runDataSize),
		0); errno != 0 {
		return fmt.Errorf("error unmapping runData: %v", errno)
	}
	return nil
}

// notify notifies that the vCPU has returned to host mode.
//
// This may be called by a signal handler and therefore throws on error.
//
//go:nosplit
func (c *vCPU) notify() {
	_, _, errno := syscall.RawSyscall6(
		syscall.SYS_FUTEX,
		uintptr(unsafe.Pointer(&c.state)),
		linux.FUTEX_WAKE,
		^uintptr(0), // Number of waiters.
		0, 0, 0)
	if errno != 0 {
		throw("futex wake error")
	}
}

// wait waits for the vCPU to return to host mode.
//
// This panics on error.
func (c *vCPU) wait() {
	if !atomic.CompareAndSwapUintptr(&c.state, vCPUGuest, vCPUWaiter) {
		return // Nothing to wait for.
	}
	for {
		_, _, errno := syscall.Syscall6(
			syscall.SYS_FUTEX,
			uintptr(unsafe.Pointer(&c.state)),
			linux.FUTEX_WAIT,
			uintptr(vCPUWaiter), // Expected value.
			0, 0, 0)
		if errno == syscall.EINTR {
			continue
		} else if errno == syscall.EAGAIN {
			break
		} else if errno != 0 {
			panic("futex wait error")
		}
		break
	}
}