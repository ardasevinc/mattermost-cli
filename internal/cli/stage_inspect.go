package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/internal/output"
	"github.com/ardasevinc/mattermost-cli/internal/presentation"
	"github.com/ardasevinc/mattermost-cli/internal/stagecursor"
	"github.com/ardasevinc/mattermost-cli/internal/stageoutput"
	"github.com/ardasevinc/mattermost-cli/internal/stagestore"
)

type localStateFailure struct{ err error }

func (e localStateFailure) Error() string { return e.err.Error() }
func (e localStateFailure) Unwrap() error { return e.err }

func newStageCommand(state *rootState) *cobra.Command {
	var fromJSON bool
	command := &cobra.Command{
		Use:   "stage",
		Short: "Create and inspect staged Mattermost changes",
		Args:  cobra.NoArgs,
	}
	command.RunE = func(cmd *cobra.Command, _ []string) error {
		if fromJSON {
			return runStructuredStage(cmd, state)
		}
		if state.flags.json {
			return invalidFailure("--json requires a stage subcommand")
		}
		return cmd.Help()
	}
	command.PersistentPreRunE = func(cmd *cobra.Command, _ []string) error {
		if fromJSON {
			state.flags.json = true
			if cmd != command && cmd.Name() != "revise" && cmd.Name() != "cancel" && cmd.Name() != "prune" {
				return invalidFailure("--from-json cannot be combined with a stage subcommand")
			}
		}
		return resolveStageOptions(state, cmd)
	}
	command.PersistentFlags().BoolVar(&fromJSON, "from-json", false, "read one versioned stage request from stdin")
	command.AddCommand(newStageListCommand(state), newStageShowCommand(state))
	command.AddCommand(newStageCreationCommands(state)...)
	command.AddCommand(newStageManagementCommands(state, &fromJSON)...)
	return command
}

func newStageListCommand(state *rootState) *cobra.Command {
	var limit int
	var cursor string
	command := &cobra.Command{
		Use:   "list",
		Short: "List staged changes without revealing content",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if limit < 1 || limit > 100 {
				return invalidFailure("--limit must be between 1 and 100")
			}
			var after *stagecursor.Boundary
			if flagChanged(cmd, "cursor") {
				if strings.TrimSpace(cursor) == "" {
					return invalidFailure("--cursor cannot be empty")
				}
				decoded, err := stagecursor.Decode(cursor)
				if err != nil {
					return invalidFailure("invalid stage cursor")
				}
				after = &decoded
			}
			store, absent, err := openStageStoreReadOnly(cmd, state)
			if err != nil {
				return err
			}
			if absent {
				return writeStages(state, stageoutput.Stages{Schema: "mm/v2/stages", Stages: []stageoutput.Summary{}})
			}
			defer store.Close()
			page, err := store.ListRecords(cmd.Context(), stagestore.ListOptions{Limit: limit, After: after})
			if err != nil {
				return localStateFailure{fmt.Errorf("could not list stages")}
			}
			document, err := stageoutput.NewStages(page, state.credentials)
			if err != nil {
				return localStateFailure{fmt.Errorf("stored stage data is invalid")}
			}
			return writeStages(state, document)
		},
	}
	command.Flags().IntVar(&limit, "limit", 50, "maximum stages to return (1-100)")
	command.Flags().StringVar(&cursor, "cursor", "", "resume deterministic stage history")
	return command
}

func newStageShowCommand(state *rootState) *cobra.Command {
	return &cobra.Command{
		Use:   "show <stage-id>",
		Short: "Show a stage, including retained content and attachment paths",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			store, absent, err := openStageStoreReadOnly(cmd, state)
			if err != nil {
				return err
			}
			if absent {
				return localStateFailure{fmt.Errorf("stage not found")}
			}
			defer store.Close()
			detail, err := store.Show(cmd.Context(), args[0])
			if errors.Is(err, stagestore.ErrInvalid) {
				return invalidFailure("invalid stage id")
			}
			if errors.Is(err, stagestore.ErrNotFound) {
				return localStateFailure{fmt.Errorf("stage not found")}
			}
			if err != nil {
				return localStateFailure{fmt.Errorf("could not read stage")}
			}
			document, err := stageoutput.NewStage(detail, state.credentials)
			if err != nil {
				return localStateFailure{fmt.Errorf("stored stage data is invalid")}
			}
			if state.flags.json {
				return writeStageJSON(state, document)
			}
			if err := writeAll(state.streams.err, []byte("warning: stage show reveals retained message content and attachment paths\n")); err != nil {
				return err
			}
			return writeStageHuman(state, document)
		},
	}
}

func openStageStoreReadOnly(cmd *cobra.Command, state *rootState) (*stagestore.Store, bool, error) {
	paths, err := storePaths(state)
	if err != nil {
		return nil, false, err
	}
	if _, err = os.Stat(paths.DBPath); errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	} else if err != nil {
		return nil, false, localStateFailure{fmt.Errorf("could not inspect stage store")}
	}
	store, err := stagestore.OpenReadOnly(cmd.Context(), paths.DBPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, localStateFailure{fmt.Errorf("could not safely open stage store")}
	}
	return store, false, nil
}

func writeStageJSON(state *rootState, document any) error {
	if _, err := output.WriteBoundedJSON(state.streams.out, document); err != nil {
		return outputError{err: err}
	}
	return nil
}

func writeStages(state *rootState, document stageoutput.Stages) error {
	if state.flags.json {
		return writeStageJSON(state, document)
	}
	var lines []string
	for _, stage := range document.Stages {
		lines = append(lines, strings.Join([]string{
			safeStoreValue(state, stage.StageRef),
			safeStoreValue(state, stage.Operation),
			safeStoreValue(state, stage.Lifecycle),
			"recovery=" + safeStoreValue(state, stage.Recovery),
			"updated=" + safeStoreValue(state, stage.UpdatedAt),
		}, "\t"))
	}
	if len(lines) == 0 {
		lines = append(lines, "no stages")
	}
	if document.NextCursor != nil {
		lines = append(lines, "next cursor: "+safeStoreValue(state, *document.NextCursor))
	}
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func writeStageHuman(state *rootState, document stageoutput.Stage) error {
	destination, err := json.Marshal(document.Stage.Destination)
	if err != nil {
		return internalFailure(err)
	}
	lines := []string{
		"stage: " + safeStoreValue(state, document.Stage.StageRef),
		"operation: " + safeStoreValue(state, document.Stage.Operation),
		"lifecycle: " + safeStoreValue(state, document.Stage.Lifecycle),
		"recovery: " + safeStoreValue(state, document.Stage.Recovery),
		"destination: " + safeStoreValue(state, string(destination)),
		"content state: " + safeStoreValue(state, document.Content.State),
	}
	if document.Content.Body != nil {
		lines = append(lines, "body:\n"+safeStageContent(state, *document.Content.Body))
	}
	lines = append(lines, "attachment state: "+safeStoreValue(state, document.AttachmentState))
	for index, attachment := range document.Attachments {
		prefix := "attachment " + strconv.Itoa(index+1) + ": "
		lines = append(lines, prefix+safeStoreValue(state, attachment.Path), "  canonical: "+safeStoreValue(state, attachment.CanonicalPath), "  remote filename: "+safeStoreValue(state, attachment.RemoteFilename))
	}
	lines = append(lines, "plan:")
	for _, step := range document.Plan.Steps {
		lines = append(lines, fmt.Sprintf("  %d. %s (%s)", step.Ordinal, safeStoreValue(state, step.Type), safeStoreValue(state, step.Condition)))
	}
	apply := "unavailable"
	if document.Stage.Lifecycle == string(stagestore.LifecycleOpen) {
		switch document.Stage.Recovery {
		case string(stagestore.RecoveryNone):
			apply = "mm apply " + safeStoreValue(state, document.Stage.StageRef)
		case string(stagestore.RecoveryPartial):
			apply = "mm apply " + safeStoreValue(state, document.Stage.StageRef) + " --resume-partial"
		case string(stagestore.RecoveryUnknown):
			apply = "mm apply " + safeStoreValue(state, document.Stage.StageRef) + " --force-unknown"
		}
	}
	lines = append(lines, "apply: "+apply)
	return writeAll(state.streams.out, []byte(strings.Join(lines, "\n")+"\n"))
}

func safeStageContent(state *rootState, value string) string {
	return presentation.PreprocessWithOptions(value, presentation.Options{
		Credentials: state.credentials, DisableHeuristics: state.disableHeuristics,
	}).Text
}
