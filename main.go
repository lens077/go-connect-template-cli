// Command co 是 go-connect-template 的脚手架工具。
package main

import (
	"os"

	"github.com/lens077/go-connect-template-cli/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
