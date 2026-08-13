package cli

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ardasevinc/mattermost-cli/v2/internal/filedownload"
	"github.com/ardasevinc/mattermost-cli/v2/internal/output"
)

type fileDownloadFlags struct {
	output, maxSize string
}

func newFileCommand(state *rootState) *cobra.Command {
	command := &cobra.Command{Use: "file", Short: "Work with Mattermost files", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	flags := new(fileDownloadFlags)
	download := &cobra.Command{
		Use: "download <file-id>", Short: "Download one explicitly requested Mattermost file", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error { return runFileDownload(cmd, state, args[0], *flags) },
	}
	download.Flags().StringVar(&flags.output, "output", "", "exact destination file (must not exist)")
	download.Flags().StringVar(&flags.maxSize, "max-size", "512MiB", "maximum download size in bytes or KiB, MiB, GiB")
	command.AddCommand(download)
	return command
}

func runFileDownload(cmd *cobra.Command, state *rootState, fileID string, flags fileDownloadFlags) error {
	maxBytes, err := parseByteSize(flags.maxSize)
	if err != nil {
		return invalidFailure("--max-size must be a positive integer with an optional B, KiB, MiB, or GiB suffix")
	}
	if flagChanged(cmd, "output") && strings.TrimSpace(flags.output) == "" {
		return invalidFailure("--output cannot be empty")
	}
	runtime, err := state.runtimeFor(cmd)
	if err != nil {
		return err
	}
	if err = emitRedactionWarning(state, runtime, state.flags.json); err != nil {
		return err
	}
	result, err := filedownload.Download(cmd.Context(), runtime.Files, fileID, filedownload.Options{
		Output: flags.output, MaxBytes: maxBytes, Presentation: identityOptions(runtime),
	})
	if err != nil {
		switch {
		case errors.Is(err, filedownload.ErrDestinationExists):
			return invalidFailure("--output destination already exists")
		case errors.Is(err, filedownload.ErrFileTooLarge):
			return invalidFailure("Mattermost file exceeds --max-size")
		default:
			return readFailure(err)
		}
	}
	document, err := output.NewFileDownloadEnvelope(result.FileID, result.Name, result.MIMEType, result.SizeBytes, result.SHA256, result.Path, result.Temporary)
	if err != nil {
		cleanupTemporaryDownload(result)
		return readFailure(err)
	}
	if state.flags.json {
		var wire bytes.Buffer
		if _, err := output.WriteMachineJSON(&wire, document); err != nil {
			return readFailure(err)
		}
		if err := writeAll(state.streams.out, wire.Bytes()); err != nil {
			cleanupTemporaryDownload(result)
			return err
		}
		return nil
	}
	var receipt strings.Builder
	fmt.Fprintf(&receipt, "Downloaded %s (%s)\nSaved to %s\n", document.Name, humanBytes(document.SizeBytes), document.Path)
	if document.Temporary {
		receipt.WriteString("Temporary file; the OS may clean it up.\n")
	}
	if err := writeAll(state.streams.out, []byte(receipt.String())); err != nil {
		cleanupTemporaryDownload(result)
		return err
	}
	return nil
}

func cleanupTemporaryDownload(result filedownload.Result) {
	if result.Temporary {
		_ = os.RemoveAll(filepath.Dir(result.Path))
	}
}

func parseByteSize(raw string) (int64, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return 0, errors.New("empty size")
	}
	factor := uint64(1)
	lower := strings.ToLower(value)
	for _, unit := range []struct {
		suffix     string
		multiplier uint64
	}{{"gib", 1 << 30}, {"mib", 1 << 20}, {"kib", 1 << 10}, {"gb", 1_000_000_000}, {"mb", 1_000_000}, {"kb", 1_000}, {"b", 1}} {
		if strings.HasSuffix(lower, unit.suffix) {
			factor = unit.multiplier
			value = strings.TrimSpace(value[:len(value)-len(unit.suffix)])
			break
		}
	}
	integer, err := strconv.ParseUint(value, 10, 64)
	if err != nil || integer == 0 || integer > uint64(output.MaxSafeMachineInteger)/factor {
		return 0, errors.New("invalid size")
	}
	return int64(integer * factor), nil
}

func humanBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d bytes", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	unit := "bytes"
	for _, candidate := range units {
		size /= 1024
		unit = candidate
		if size < 1024 || candidate == units[len(units)-1] {
			break
		}
	}
	precision := 1
	if math.Trunc(size) == size {
		precision = 0
	}
	return strconv.FormatFloat(size, 'f', precision, 64) + " " + unit
}
