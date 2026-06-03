package commands

import (
	"fmt"
	"os"

	"co-cli/pkg/template"

	"github.com/spf13/cobra"
)

var protoServerOptions = struct {
	targetDir string
}{}

var protoCmd = &cobra.Command{
	Use:   "proto <command>",
	Short: "Proto file generation commands",
	Long:  `Commands for working with protobuf files.`,
}

var protoAddCmd = &cobra.Command{
	Use:   "add <proto-path>",
	Short: "Add a new proto file",
	Long:  `Create a new proto file with default service definition.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		protoPath := args[0]
		opts := template.ProtoAddOptions{
			ProtoPath: protoPath,
		}
		if err := template.AddProto(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to add proto file: %v\n", err)
			os.Exit(1)
		}
	},
}

var protoServerCmd = &cobra.Command{
	Use:   "server <proto-path>",
	Short: "Generate proto server code",
	Long:  `Generate Connect RPC server code from a proto file.`,
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		protoPath := args[0]
		opts := template.ProtoServerOptions{
			ProtoPath: protoPath,
			TargetDir: protoServerOptions.targetDir,
		}
		if err := template.GenerateProtoServer(opts); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to generate server code: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	protoCmd.AddCommand(protoAddCmd)
	protoCmd.AddCommand(protoServerCmd)
	protoServerCmd.Flags().StringVarP(&protoServerOptions.targetDir, "target", "t", "internal/service", "Target directory for server code")
}
