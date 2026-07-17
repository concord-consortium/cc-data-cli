package cmd

import (
	"errors"

	"github.com/concord-consortium/cc-data-cli/internal/dataset"
	"github.com/concord-consortium/cc-data-cli/internal/output"
	"github.com/spf13/cobra"
)

func newDatasetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dataset",
		Short: "Create and manage local datasets",
	}
	cmd.AddCommand(
		newDatasetCreateCmd(),
		newDatasetRenameCmd(),
		newDatasetEditCmd(),
		newDatasetDeleteCmd(),
		newDatasetPurgeCmd(),
		newDatasetShowCmd(),
		newDatasetListCmd(),
		newReindexCmd(),
	)
	return cmd
}

func newDatasetCreateCmd() *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "create [<portal>/<name>]",
		Short: "Create a new dataset",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			var ref dataset.Ref
			if len(args) == 1 {
				ref, err = resolveRef(cfg, args[0])
				if err != nil {
					return err
				}
			} else {
				if cfg.DefaultPortal == "" {
					return output.Usagef("no dataset name given and no default_portal configured")
				}
				name, err := dataset.AutoName(root, cfg.DefaultPortal, description)
				if err != nil {
					return err
				}
				ref = dataset.Ref{Portal: cfg.DefaultPortal, Name: name}
			}
			echoRef(ref)
			if _, err := dataset.Create(root, ref, description); err != nil {
				if errors.Is(err, dataset.ErrBusy) {
					return output.Busyf("%v", err)
				}
				return output.Usagef("%v", err)
			}
			output.Progressf("created dataset %s", ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "dataset description (also seeds an auto-generated name)")
	return cmd
}

func newDatasetRenameCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rename <ref> <new-name>",
		Short: "Rename a dataset",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, args[0])
			if err != nil {
				return err
			}
			echoRef(ref)
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return notFound(ref)
			}
			newD, err := d.Rename(root, args[1])
			if err != nil {
				return mutationErr(err)
			}
			output.Progressf("renamed %s to %s", ref, newD.Ref)
			return nil
		},
	}
	return cmd
}

func newDatasetEditCmd() *cobra.Command {
	var description string
	cmd := &cobra.Command{
		Use:   "edit <ref> --description <text>",
		Short: "Edit a dataset's description",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("description") {
				return output.Usagef("--description is required")
			}
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, args[0])
			if err != nil {
				return err
			}
			echoRef(ref)
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return notFound(ref)
			}
			if err := d.Edit(description); err != nil {
				return mutationErr(err)
			}
			output.Progressf("updated description for %s", ref)
			return nil
		},
	}
	cmd.Flags().StringVar(&description, "description", "", "new description")
	return cmd
}

func newDatasetDeleteCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "delete <ref>",
		Short: "Delete a dataset folder (local only)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, args[0])
			if err != nil {
				return err
			}
			echoRef(ref)
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return notFound(ref)
			}
			if !force && !confirm("Delete dataset "+ref.String()+" and all its data?") {
				output.Progressf("aborted")
				return nil
			}
			if err := d.Delete(); err != nil {
				return mutationErr(err)
			}
			output.Progressf("deleted %s", ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func newDatasetPurgeCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "purge <ref>",
		Short: "Delete a dataset's downloaded data but keep the dataset",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, root, err := loadRuntime()
			if err != nil {
				return err
			}
			ref, err := resolveRef(cfg, args[0])
			if err != nil {
				return err
			}
			echoRef(ref)
			d := dataset.Open(root, ref)
			if !d.Exists() {
				return notFound(ref)
			}
			if !force && !confirm("Purge all downloaded data from "+ref.String()+"?") {
				output.Progressf("aborted")
				return nil
			}
			if err := d.Purge(); err != nil {
				return mutationErr(err)
			}
			output.Progressf("purged %s", ref)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "skip confirmation")
	return cmd
}

func notFound(ref dataset.Ref) error {
	return &output.CLIError{ExitCode: output.ExitUsage, Code: "NOT_FOUND", Message: "dataset " + ref.String() + " does not exist"}
}

func mutationErr(err error) error {
	if errors.Is(err, dataset.ErrBusy) {
		return output.Busyf("%v", err)
	}
	return output.Internalf("%v", err)
}
