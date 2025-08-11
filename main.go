package main

import (
    "bytes"
    "encoding/json"
    "flag"
    "fmt"
    "io"
    "log"
    "net/http"
    "os"
    "os/exec"
    "sync"
    "syscall"
)

// ProcessStatus defines the possible states of the managed process.
type ProcessStatus string
const (
    StatusNotStarted ProcessStatus = "not_started"
    StatusRunning    ProcessStatus = "running"
    StatusSuccess    ProcessStatus = "success"
    StatusFailed     ProcessStatus = "failed"
)

// ProcessManager holds the state and control for the child process.
type ProcessManager struct {
    mu             sync.Mutex
    executablePath string
    args           []string
    cmd            *exec.Cmd
    status         ProcessStatus
    logBuffer      bytes.Buffer
}

// NewProcessManager creates and initializes a new manager.
func NewProcessManager(executablePath string, args []string) *ProcessManager {
    return &ProcessManager{
        executablePath: executablePath,
        args:           args,
        status:         StatusNotStarted,
    }
}

// Start launches the executable. It's safe to call on a running process.
func (pm *ProcessManager) Start() error {
    pm.mu.Lock()
    defer pm.mu.Unlock()

    // Prevent starting if it's already running.
    if pm.status == StatusRunning {
        return fmt.Errorf("process is already running")
    }

    // exec.Command now includes the arguments.
    // The '...' unpacks the slice into individual arguments.
    pm.cmd = exec.Command(pm.executablePath, pm.args...)
    pm.logBuffer.Reset()

    // Capture both stdout and stderr into our log buffer AND the os.Stdout
    // This allows us to see logs in real-time on the manager's console.
    multiWriter := io.MultiWriter(&pm.logBuffer, os.Stdout)
    pm.cmd.Stdout = multiWriter
    pm.cmd.Stderr = multiWriter

    // Start the command asynchronously.
    if err := pm.cmd.Start(); err != nil {
        pm.status = StatusFailed
        return fmt.Errorf("failed to start process: %w", err)
    }

    pm.status = StatusRunning
    log.Printf("Started process '%s %v' with PID: %d", pm.executablePath, pm.args, pm.cmd.Process.Pid)

    // Start a goroutine to wait for the process to exit and update the status.
    go pm.waitForProcess()

    return nil
}

// waitForProcess blocks until the process exits and then updates its status.
func (pm *ProcessManager) waitForProcess() {
    err := pm.cmd.Wait()

    pm.mu.Lock()
    defer pm.mu.Unlock()

    if err != nil {
        // An exit code other than 0 is considered an error.
        if exitErr, ok := err.(*exec.ExitError); ok {
            pm.status = StatusFailed
            log.Printf("Process exited with error: %v. Exit code: %d", err, exitErr.ExitCode())
        } else {
            pm.status = StatusFailed
            log.Printf("Process wait failed with error: %v", err)
        }
    } else {
        // Success (exit code 0).
        pm.status = StatusSuccess
        log.Println("Process exited successfully.")
    }
}

// Stop terminates the running process.
func (pm *ProcessManager) Stop() error {
    pm.mu.Lock()
    defer pm.mu.Unlock()

    if pm.status != StatusRunning {
        return fmt.Errorf("process is not running")
    }

    // Send a SIGTERM signal. This is a graceful shutdown signal.
    if err := pm.cmd.Process.Signal(syscall.SIGTERM); err != nil {
        return fmt.Errorf("failed to send SIGTERM to process: %w", err)
    }

    log.Printf("Sent SIGTERM to process with PID: %d", pm.cmd.Process.Pid)
    return nil
}

// GetStatus returns the current status of the process.
func (pm *ProcessManager) GetStatus() ProcessStatus {
    pm.mu.Lock()
    defer pm.mu.Unlock()
    return pm.status
}

// GetLogs returns all captured logs from the process.
func (pm *ProcessManager) GetLogs() string {
    pm.mu.Lock()
    defer pm.mu.Unlock()
    return pm.logBuffer.String()
}

// --- HTTP Handlers ---

// makeStatusHandler returns the current process status via API.
func makeStatusHandler(pm *ProcessManager) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        status := pm.GetStatus()
        log.Printf("API: /status requested. Current status: %s", status)
        w.Header().Set("Content-Type", "application/json")
        json.NewEncoder(w).Encode(map[string]string{"status": string(status)})
    }
}

// makeStartHandler starts the process via API.
func makeStartHandler(pm *ProcessManager) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
            return
        }

        err := pm.Start()
        if err != nil {
            log.Printf("API: /start failed: %v", err)
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }

        log.Println("API: /start successful.")
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("Process started successfully."))
    }
}

// makeStopHandler stops the process via API.
func makeStopHandler(pm *ProcessManager) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
            return
        }

        err := pm.Stop()
        if err != nil {
            log.Printf("API: /stop failed: %v", err)
            http.Error(w, err.Error(), http.StatusBadRequest)
            return
        }
        log.Println("API: /stop successful.")
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("Process stop signal sent."))
    }
}

func makeExitHandler(pm *ProcessManager) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        if r.Method != http.MethodPost {
            http.Error(w, "Invalid request method", http.StatusMethodNotAllowed)
            return
        }

        pm.Stop()
        w.WriteHeader(http.StatusOK)
        w.Write([]byte("Process stop signal sent."))
        w.Write([]byte("Exit"))
        os.Exit(0)
    }
}

// makeLogHandler returns the process logs via API.
func makeLogHandler(pm *ProcessManager) http.HandlerFunc {
    return func(w http.ResponseWriter, r *http.Request) {
        logs := pm.GetLogs()
        log.Println("API: /logs requested.")
        w.Header().Set("Content-Type", "text/plain")
        w.Write([]byte(logs))
    }
}

func main() {
    port := flag.String("port", "8080", "Port for the web server")
	flag.Parse()

	args := flag.Args()
    if len(args) < 1 {
        log.Fatal("Usage: gowork -port <port> <executable_path> [arg1] [arg2] ...")
    }
	executablePath := args[0]
	executableArgs := args[1:]

	if _, err := os.Stat(executablePath); os.IsNotExist(err) {
		log.Fatalf("Executable file not found at: %s", executablePath)
	}

	log.Printf("Managing executable: %s with args: %v", executablePath, executableArgs)
	manager := NewProcessManager(executablePath, executableArgs)

	if err := manager.Start(); err != nil {
		log.Printf("Initial start failed: %v", err)
	}

	http.HandleFunc("/status", makeStatusHandler(manager))
	http.HandleFunc("/start", makeStartHandler(manager))
	http.HandleFunc("/stop", makeStopHandler(manager))
	http.HandleFunc("/log", makeLogHandler(manager))
	http.HandleFunc("/exit", makeExitHandler(manager))

	log.Printf("Starting server on port %s...", *port)
	if err := http.ListenAndServe(":" + *port, nil); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
