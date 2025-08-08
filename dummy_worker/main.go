package main

import (
    "fmt"
    "os"
    "time"
)

func main() {
    fmt.Println("stdout: Dummy worker started successfully.")
    os.Stderr.WriteString("stderr: This is a sample error log.\n")

    for i := 1; i <= 5; i++ {
        fmt.Printf("stdout: Worker is doing work... step %d/5\n", i)
        time.Sleep(1 * time.Second)
    }

    fmt.Println("stdout: Dummy worker finished.")
}
