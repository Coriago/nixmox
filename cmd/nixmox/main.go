package main

import (
	"fmt"
	"os"

	"nixmox/internal/domain"
)

func main() {
	fmt.Fprintln(os.Stderr, domain.Unavailable("command interface"))
	os.Exit(3)
}
