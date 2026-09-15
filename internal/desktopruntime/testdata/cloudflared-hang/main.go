// A deliberately non-ready child for Windows Tunnel cancellation regression.
package main

import (
	"fmt"
	"os"
	"time"
)

func main() {
	fmt.Printf("cloudflared cancellation fixture pid=%d\n", os.Getpid())
	time.Sleep(time.Hour)
}
