package ormconnector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/lemmego/api/app"
	"github.com/lemmego/orm/ormgen"
	"github.com/spf13/cobra"
)

// genFieldsCmd exposes the descriptor generator as `orm:fields`.
//
// The command lives here rather than in the CLI module because generation must
// use the ORM's own naming rules, and the CLI does not depend on the ORM. The
// framework's CommandProvider hook is exactly the seam for this.
var genFieldsCmd = func(a app.App) *cobra.Command {
	var (
		outFile   string
		typeNames string
		all       bool
	)

	command := &cobra.Command{
		Use:   "orm:fields [directory]",
		Short: "Generate typed ORM field descriptors",
		Long: "Generate typed field descriptors for ORM models, giving compile-time\n" +
			"checked queries such as db.Model[User]().Where(UserFields.Active.Eq(true)).\n\n" +
			"Descriptors are optional: the same query can always be written with the\n" +
			"string constructors, for example orm.Eq(\"active\", true).",
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "./internal/models"
			if len(args) > 0 {
				dir = args[0]
			}

			options := ormgen.Options{Dir: dir, All: all}
			for _, name := range strings.Split(typeNames, ",") {
				if trimmed := strings.TrimSpace(name); trimmed != "" {
					options.Only = append(options.Only, trimmed)
				}
			}

			source, err := ormgen.Generate(options)
			if err != nil {
				return err
			}

			path := filepath.Join(dir, outFile)
			if err := os.WriteFile(path, source, 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Generated %s\n", path)
			return nil
		},
	}

	command.Flags().StringVarP(&outFile, "out", "o", ormgen.OutputFileName, "output file name")
	command.Flags().StringVarP(&typeNames, "type", "t", "", "comma-separated model names to generate for")
	command.Flags().BoolVar(&all, "all", false, "generate for every exported struct, not just tagged models")
	return command
}
