//go:build windows

// Package deskpower answers the two questions a sync client asks before it
// starts transferring on its own schedule: is this machine on battery, and is
// this connection one the user pays for by the byte.
//
// Both answers are best effort and both fail open — "unknown" means go ahead.
// A sync client that pauses because it could not determine the power state is
// broken in a way that is hard to notice and impossible to explain.
package deskpower

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")

	procGetSystemPowerStatus = kernel32.NewProc("GetSystemPowerStatus")
	procCoInitializeEx       = ole32.NewProc("CoInitializeEx")
	procCoUninitialize       = ole32.NewProc("CoUninitialize")
	procCoCreateInstance     = ole32.NewProc("CoCreateInstance")
)

// systemPowerStatus is SYSTEM_POWER_STATUS.
type systemPowerStatus struct {
	ACLineStatus        byte
	BatteryFlag         byte
	BatteryLifePercent  byte
	SystemStatusFlag    byte
	BatteryLifeTime     uint32
	BatteryFullLifeTime uint32
}

// OnBattery reports whether the machine is running on battery.
//
// ACLineStatus is 0 offline, 1 online, 255 unknown. Only a definite 0 counts:
// a desktop with no battery reports online, and an unknown state must not pause
// anything.
func OnBattery() bool {
	var status systemPowerStatus
	ok, _, _ := procGetSystemPowerStatus.Call(uintptr(unsafe.Pointer(&status)))
	if ok == 0 {
		return false
	}
	return status.ACLineStatus == 0
}

// Network cost, via the Network List Manager.
//
// There is no plain C function for this: Windows exposes it as COM
// (INetworkCostManager) or WinRT (NetworkInformation). COM is used here because
// it can be reached with three calls and two vtable offsets, while the WinRT
// projection would mean activation factories and IInspectable — a great deal of
// machinery for one integer.
var (
	clsidNetworkListManager = windows.GUID{
		Data1: 0xDCB00C01, Data2: 0x570F, Data3: 0x4A9B,
		Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B},
	}
	iidNetworkCostManager = windows.GUID{
		Data1: 0xDCB00008, Data2: 0x570F, Data3: 0x4A9B,
		Data4: [8]byte{0x8D, 0x69, 0x19, 0x9F, 0xDB, 0xA5, 0x72, 0x3B},
	}
)

// NLM_CONNECTION_COST flags. Anything beyond unrestricted means the connection
// is limited, charged, or over its allowance.
const (
	costUnknown      = 0x0
	costUnrestricted = 0x1
	costFixed        = 0x2
	costVariable     = 0x4
	// The remaining bits — over data limit, congested, roaming, approaching
	// limit — all mean "be careful", which for us is the same decision.
	costOverDataLimit = 0x10000
	costCongested     = 0x20000
	costRoaming       = 0x40000
	costApproaching   = 0x80000
)

const (
	coinitApartmentThreaded = 0x2
	clsctxAll               = 0x17
)

// Metered reports whether the current connection is metered.
//
// False on any failure: an unavailable Network List Manager, a machine with no
// connection, or a cost the service could not determine. Refusing to sync
// because a COM call failed would be the wrong trade.
func Metered() bool {
	// Apartment-threaded, and uninitialised again on the way out: this is called
	// from a background goroutine that does not own the process's COM state.
	hr, _, _ := procCoInitializeEx.Call(0, coinitApartmentThreaded)
	// S_OK or S_FALSE (already initialised on this thread) are both usable;
	// RPC_E_CHANGED_MODE means somebody else set a different model, and the
	// call is still worth trying without our own uninitialise.
	initialised := hr == 0 || hr == 1
	if initialised {
		defer procCoUninitialize.Call() //nolint:errcheck // nothing actionable
	}

	var manager *iNetworkCostManager
	ret, _, _ := procCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidNetworkListManager)),
		0,
		clsctxAll,
		uintptr(unsafe.Pointer(&iidNetworkCostManager)),
		uintptr(unsafe.Pointer(&manager)),
	)
	if ret != 0 || manager == nil {
		return false
	}
	defer manager.Release()

	var cost uint32
	if !manager.GetCost(&cost) {
		return false
	}
	switch {
	case cost == costUnknown, cost == costUnrestricted:
		return false
	case cost&(costFixed|costVariable) != 0:
		return true
	case cost&(costOverDataLimit|costCongested|costRoaming|costApproaching) != 0:
		return true
	}
	return false
}

// iNetworkCostManager is the COM object, reached through its vtable.
//
// Only the first three IUnknown slots and GetCost are needed, so the rest of the
// interface is left undeclared rather than transcribed for completeness — an
// unused function pointer at the wrong offset is a crash waiting for a caller.
type iNetworkCostManager struct {
	vtbl *iNetworkCostManagerVtbl
}

type iNetworkCostManagerVtbl struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetCost        uintptr
	// GetDataPlanStatus and SetDestinationAddresses follow; unused.
}

func (m *iNetworkCostManager) Release() {
	_, _, _ = syscallN(m.vtbl.Release, uintptr(unsafe.Pointer(m)))
}

// GetCost fills cost for the machine's default connection. The second argument
// is a destination address; NULL asks about the default route, which is the
// connection a sync would actually use.
func (m *iNetworkCostManager) GetCost(cost *uint32) bool {
	hr, _, _ := syscallN(m.vtbl.GetCost,
		uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(cost)), 0)
	return hr == 0
}

// syscallN calls a COM method through its vtable pointer.
//
// syscall.SyscallN takes a function address, which is exactly what a vtable slot
// holds; wrapping it keeps the call shape in one place instead of at every site.
func syscallN(trap uintptr, args ...uintptr) (r1, r2 uintptr, err error) {
	return syscall.SyscallN(trap, args...)
}
