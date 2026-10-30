package daemon

import (
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Tacrolimus/multi-dap/internal/config"
	"github.com/Tacrolimus/multi-dap/internal/tracebridge"
	"github.com/Tacrolimus/multi-dap/internal/tracecontrol"
)

const maxTraceLibraryBytes = 64 << 20

// Native trace is optional. Discovery is host-file inspection only; ordinary
// DAP startup must not load the DLL, connect a trace session, or fail merely
// because the separately selected native runtime is unavailable.
func prepareNativeTraceController(configuration config.Validated, bridgeScript string, probe probeLock) *tracecontrol.Controller {
	if runtime.GOOS != "windows" || configuration.Multi.Version != "8.1.4" || configuration.Multi.PythonVersion != 3 {
		return nil
	}
	installation, err := filepath.EvalSymlinks(configuration.Multi.Installation)
	if err != nil {
		return nil
	}
	installation, err = filepath.Abs(installation)
	if err != nil {
		return nil
	}
	library, err := containedNativeFile(installation, "timemachine_api.dll")
	if err != nil {
		return nil
	}
	digest, err := inspectNativeLibrary(library)
	if err != nil {
		return nil
	}
	python := ""
	for _, candidate := range []string{"python-3.8.6/python.exe", "python3/python.exe", "python38/python.exe"} {
		if selected, err := containedNativeFile(installation, filepath.FromSlash(candidate)); err == nil {
			python = selected
			break
		}
	}
	if python == "" {
		return nil
	}
	helper, err := regularBridgeScript(filepath.Join(filepath.Dir(bridgeScript), "timemachine_helper.py"))
	if err != nil {
		return nil
	}
	if _, err := regularBridgeScript(filepath.Join(filepath.Dir(bridgeScript), "timemachine_native.py")); err != nil {
		return nil
	}
	controller, err := tracecontrol.New(tracecontrol.Options{
		Installation: installation, MultiVersion: configuration.Multi.Version, DLLSHA256: digest,
		AcquireSource: func(ctx context.Context, id uint32) (io.Closer, error) {
			return acquireNativeTraceSourceForProbe(ctx, id, probe)
		},
		Factory: func(ctx context.Context, lease io.Closer) (tracecontrol.Backend, error) {
			owned, ok := lease.(nativeSourceLease)
			if !ok {
				return nil, tracebridge.ErrInvalidOptions
			}
			client, err := tracebridge.Start(ctx, tracebridge.Options{
				Python: python, Script: helper, Installation: installation,
				ExpectedPointerBits: 64, ExpectedInfoSize: 64,
				StartupTimeout:   boundedNativeDeadline(configuration.Timing.StartupDeadline, 5*time.Second),
				RPCTimeout:       boundedNativeDeadline(configuration.Timing.RPCDeadline, 3*time.Second),
				ShutdownTimeout:  2 * time.Second,
				InheritedHandles: owned.InheritedHandles(),
			})
			if client == nil {
				return nil, err
			}
			return client, err
		},
	})
	if err != nil {
		return nil
	}
	return controller
}

func boundedNativeDeadline(configured, fallback time.Duration) time.Duration {
	if configured <= 0 {
		return fallback
	}
	return min(configured, time.Minute)
}

func containedNativeFile(root, relative string) (string, error) {
	path, err := filepath.EvalSymlinks(filepath.Join(root, relative))
	if err != nil {
		return "", err
	}
	within, err := filepath.Rel(root, path)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) || filepath.IsAbs(within) {
		return "", errors.New("native file leaves installation")
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", errors.New("native file is unavailable")
	}
	return path, nil
}

func inspectNativeLibrary(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxTraceLibraryBytes {
		return "", errors.New("native library is unavailable")
	}
	image, err := pe.NewFile(file)
	if err != nil {
		return "", errors.New("native library is not PE")
	}
	defer image.Close()
	if image.Machine != pe.IMAGE_FILE_MACHINE_AMD64 || image.Characteristics&pe.IMAGE_FILE_DLL == 0 {
		return "", errors.New("native library architecture is unsupported")
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(io.NewSectionReader(file, 0, maxTraceLibraryBytes+1), maxTraceLibraryBytes+1))
	if err != nil || size != info.Size() || size > maxTraceLibraryBytes {
		return "", errors.New("native library changed while inspecting")
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

type nativeSourceLease interface {
	io.Closer
	InheritedHandles() []uintptr
}

func acquireNativeTraceSource(ctx context.Context, sessionID uint32) (io.Closer, error) {
	return acquireNativeTraceSourceForProbe(ctx, sessionID, nil)
}

func acquireNativeTraceSourceForProbe(ctx context.Context, sessionID uint32, probe probeLock) (io.Closer, error) {
	if sessionID == 0 || sessionID > 1<<31-1 {
		return nil, tracecontrol.ErrInvalidRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Session IDs lack a proven installation/core namespace. Conservatively
	// exclude the same numeric source across daemons in this OS logon session,
	// rather than assuming two installation paths identify separate sources.
	lock, err := acquireProbeLock("\x00native-trace-source-v1\x00" + strconv.FormatUint(uint64(sessionID), 10))
	if err != nil {
		return nil, tracecontrol.ErrUnavailable
	}
	lease, err := inheritNativeSourceLock(lock, probe)
	if err != nil {
		_ = lock.Close()
		return nil, tracecontrol.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		_ = lease.Close()
		return nil, err
	}
	return lease, nil
}
