package main

import (
	"fmt"
	"time"

	"github.com/sendbird/ccx/internal/session"
)

func main() {
	start := time.Now()
	sessions, err := session.ScanSessions("")
	elapsed := time.Since(start)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err)
		return
	}
	fmt.Printf("Scanned %d sessions in %v\n", len(sessions), elapsed)
}
