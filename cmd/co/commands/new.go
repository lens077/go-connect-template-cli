package commands

import (
	"fmt"

	"github.com/manifoldco/promptui"
	"github.com/spf13/cobra"
	"co-cli/pkg/template"
)

type ConfigOptions struct {
	Database   string
	Cache      string
	Search     string
	IAM        string
	BuildTool  string
}

var newCmd = &cobra.Command{
	Use:   "new [service-name]",
	Short: "Create a new go-connect service",
	Long:  `Create a new go-connect service with various configuration options.`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		serviceName := args[0]
		
		noMod, _ := cmd.Flags().GetBool("nomod")
		
		// 获取标志值
		database, _ := cmd.Flags().GetString("database")
		cache, _ := cmd.Flags().GetString("cache")
		search, _ := cmd.Flags().GetString("search")
		iam, _ := cmd.Flags().GetString("iam")
		buildTool, _ := cmd.Flags().GetString("build")
		
		opts := ConfigOptions{}
		var err error
		
		// 如果没有提供标志值，则询问用户
		if database == "" {
			opts.Database, err = selectDatabase()
			if err != nil {
				return err
			}
		} else {
			opts.Database = database
		}
		
		if cache == "" {
			opts.Cache, err = selectCache()
			if err != nil {
				return err
			}
		} else {
			opts.Cache = cache
		}
		
		if search == "" {
			opts.Search, err = selectSearch()
			if err != nil {
				return err
			}
		} else {
			opts.Search = search
		}
		
		if iam == "" {
			opts.IAM, err = selectIAM()
			if err != nil {
				return err
			}
		} else {
			opts.IAM = iam
		}
		
		if buildTool == "" {
			opts.BuildTool, err = selectBuildTool()
			if err != nil {
				return err
			}
		} else {
			opts.BuildTool = buildTool
		}
		
		fmt.Println("\nCreating service with configuration:")
		fmt.Printf("  Database: %s\n", opts.Database)
		fmt.Printf("  Cache: %s\n", opts.Cache)
		fmt.Printf("  Search Engine: %s\n", opts.Search)
		fmt.Printf("  IAM: %s\n", opts.IAM)
		fmt.Printf("  Build Tool: %s\n", opts.BuildTool)
		
		templateOpts := template.NewOptions{
			Path:         serviceName,
			RepoURL:      "https://github.com/lens077/go-connect-template",
			NoMod:        noMod,
			BuildTool:    opts.BuildTool,
			Database:     opts.Database,
			Cache:        opts.Cache,
			Search:       opts.Search,
			IAM:          opts.IAM,
		}
		
		return template.CreateNewProject(templateOpts)
	},
}

func init() {
	newCmd.Flags().Bool("nomod", false, "Create service without go.mod (for monorepo)")
	newCmd.Flags().String("database", "", "Database type (mysql, postgres, sqlite, none)")
	newCmd.Flags().String("cache", "", "Cache type (redis, none)")
	newCmd.Flags().String("search", "", "Search engine (es, none)")
	newCmd.Flags().String("iam", "", "IAM provider (casdoor, none)")
	newCmd.Flags().String("build", "", "Build tool (makefile, taskfile)")
	rootCmd.AddCommand(newCmd)
}

func selectDatabase() (string, error) {
	prompt := promptui.Select{
		Label: "Select database",
		Items: []string{"mysql", "postgres", "sqlite", "none"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		return "", fmt.Errorf("prompt failed: %w", err)
	}

	return result, nil
}

func selectCache() (string, error) {
	prompt := promptui.Select{
		Label: "Select cache",
		Items: []string{"redis", "none"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		return "", fmt.Errorf("prompt failed: %w", err)
	}

	return result, nil
}

func selectSearch() (string, error) {
	prompt := promptui.Select{
		Label: "Select search engine",
		Items: []string{"es", "none"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		return "", fmt.Errorf("prompt failed: %w", err)
	}

	return result, nil
}

func selectIAM() (string, error) {
	prompt := promptui.Select{
		Label: "Select IAM",
		Items: []string{"casdoor", "none"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		return "", fmt.Errorf("prompt failed: %w", err)
	}

	return result, nil
}

func selectBuildTool() (string, error) {
	prompt := promptui.Select{
		Label: "Select build tool",
		Items: []string{"Makefile", "Taskfile"},
	}

	_, result, err := prompt.Run()
	if err != nil {
		return "", fmt.Errorf("prompt failed: %w", err)
	}

	return result, nil
}