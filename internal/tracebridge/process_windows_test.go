//go:build windows

package tracebridge

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
)

func TestHelperIsLaunchedWithoutVisibleWindow(t *testing.T) {
	command := &exec.Cmd{}
	configureProcess(command)
	if command.SysProcAttr == nil || !command.SysProcAttr.HideWindow ||
		command.SysProcAttr.CreationFlags&0x08000000 == 0 {
		t.Fatal("native helper must use hidden, no-console process creation")
	}
}

func TestSpecialWindowsPathsAreNotExecutableInputs(t *testing.T) {
	for _, path := range []string{`\\.\NUL`, `\\.\pipe\helper`, `\\?\C:\python.exe`, `C:\install\python.exe:payload`} {
		if ordinaryPath(path) {
			t.Fatal("device namespace or alternate stream accepted")
		}
	}
	for _, path := range []string{`C:\install\python.exe`, `\\host\share\python.exe`} {
		if !ordinaryPath(path) {
			t.Fatal("ordinary absolute path rejected")
		}
	}
}

func TestKillOnCloseJobHandleIsNotInherited(t *testing.T) {
	var layout extendedJobLimits
	wantSize, wantIOOffset := uintptr(144), uintptr(64)
	if unsafe.Sizeof(uintptr(0)) == 4 {
		wantSize, wantIOOffset = 112, 48
	}
	if unsafe.Sizeof(layout) != wantSize || unsafe.Offsetof(layout.IO) != wantIOOffset {
		t.Fatal("job information has the wrong Windows ABI layout")
	}
	owner, err := newProcessOwner()
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	job := owner.(*windowsProcessOwner)
	var flags uint32
	getFlags := jobKernel32.NewProc("GetHandleInformation")
	if result, _, err := getFlags.Call(uintptr(job.handle), uintptr(unsafe.Pointer(&flags))); result == 0 {
		t.Fatalf("GetHandleInformation: %v", err)
	}
	if flags&1 != 0 {
		t.Fatal("helper could inherit and keep its parent's job alive")
	}
	var limits extendedJobLimits
	query := jobKernel32.NewProc("QueryInformationJobObject")
	if result, _, err := query.Call(uintptr(job.handle), 9, uintptr(unsafe.Pointer(&limits)), unsafe.Sizeof(limits), 0); result == 0 {
		t.Fatalf("QueryInformationJobObject: %v", err)
	}
	if limits.Basic.Flags&0x00002000 == 0 {
		t.Fatal("job lacks kill-on-close protection")
	}
}

func TestParentDeathKillsHelperEvenWhenItIgnoresEOF(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	parent := exec.CommandContext(ctx, executable)
	parent.Env = append(os.Environ(), fakeHelperMode+"=job_parent")
	configureProcess(parent)
	output, err := parent.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	input, err := parent.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := parent.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		if parent.ProcessState == nil {
			_ = parent.Process.Kill()
			_ = parent.Wait()
		}
	})
	line, err := bufio.NewReader(io.LimitReader(output, 1024)).ReadString('\n')
	if err != nil {
		t.Fatalf("ownership fixture did not report its child: %v", err)
	}
	var child struct {
		PID uint32 `json:"pid"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &child); err != nil || child.PID == 0 {
		t.Fatal("invalid fixture child identity")
	}
	const synchronize = 0x00100000
	const queryLimited = 0x1000
	const terminate = 0x0001
	handle, err := syscall.OpenProcess(synchronize|queryLimited|terminate, false, child.PID)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	defer func() {
		if state, _ := syscall.WaitForSingleObject(handle, 0); state == syscall.WAIT_TIMEOUT {
			_ = syscall.TerminateProcess(handle, 99)
			_, _ = syscall.WaitForSingleObject(handle, 5000)
		}
	}()
	if state, err := syscall.WaitForSingleObject(handle, 0); err != nil || state != syscall.WAIT_TIMEOUT {
		t.Fatalf("fixture helper was not alive before parent death: %d, %v", state, err)
	}
	if _, err := io.WriteString(input, "exit without cleanup\n"); err != nil {
		t.Fatal(err)
	}
	if err := parent.Wait(); err != nil {
		t.Fatalf("fixture parent exit failed: %v", err)
	}
	if state, err := syscall.WaitForSingleObject(handle, 5000); err != nil || state != syscall.WAIT_OBJECT_0 {
		t.Fatalf("parent death left its native helper alive: %d, %v", state, err)
	}
}

func TestExplicitInheritedLeaseOutlivesParentHandlesUntilHelperExit(t *testing.T) {
	t.Setenv(fakeHelperMode, "happy")
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	name := `Local\multi-dap-tracebridge-fixture-` + hex.EncodeToString(random[:])
	name16, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	create := jobKernel32.NewProc("CreateSemaphoreW")
	open := jobKernel32.NewProc("OpenSemaphoreW")
	handle, _, err := create.Call(0, 0, 1, uintptr(unsafe.Pointer(name16)))
	if handle == 0 {
		t.Fatalf("CreateSemaphoreW: %v", err)
	}
	original := syscall.Handle(handle)
	defer func() {
		if original != 0 {
			_ = syscall.CloseHandle(original)
		}
	}()
	if validInheritedHandles([]uintptr{handle}) {
		t.Fatal("non-inheritable lease was accepted")
	}
	current, _, _ := jobKernel32.NewProc("GetCurrentProcess").Call()
	var duplicate syscall.Handle
	duplicateHandle := jobKernel32.NewProc("DuplicateHandle")
	if result, _, err := duplicateHandle.Call(
		current, handle, current, uintptr(unsafe.Pointer(&duplicate)), 0, 1, 2,
	); result == 0 {
		t.Fatalf("DuplicateHandle: %v", err)
	}
	defer func() {
		if duplicate != 0 {
			_ = syscall.CloseHandle(duplicate)
		}
	}()
	options := fakeOptions(t)
	options.InheritedHandles = []uintptr{uintptr(duplicate)}
	client, err := Start(context.Background(), options)
	trackClient(t, client)
	if err != nil {
		t.Fatal(err)
	}
	retained := uintptr(duplicate)
	options.InheritedHandles[0] = 0
	if len(client.options.InheritedHandles) != 1 || client.options.InheritedHandles[0] != retained {
		t.Fatal("caller mutation changed the process's selected lease handles")
	}
	if err := syscall.CloseHandle(original); err != nil {
		t.Fatal(err)
	}
	original = 0
	if err := syscall.CloseHandle(duplicate); err != nil {
		t.Fatal(err)
	}
	duplicate = 0
	const synchronize = 0x00100000
	proof, _, err := open.Call(synchronize, 0, uintptr(unsafe.Pointer(name16)))
	if proof == 0 {
		t.Fatalf("helper did not retain its inherited source lease: %v", err)
	}
	_ = syscall.CloseHandle(syscall.Handle(proof))
	client.Abort()
	if err := client.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	remaining, _, _ := open.Call(synchronize, 0, uintptr(unsafe.Pointer(name16)))
	if remaining != 0 {
		_ = syscall.CloseHandle(syscall.Handle(remaining))
		t.Fatal("helper reaping left an inherited source lease alive")
	}
}
