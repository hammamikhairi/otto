package conversation

import (
	"context"
	"fmt"

	"github.com/hammamikhairi/ottocook/internal/domain"
	"github.com/hammamikhairi/ottocook/internal/logger"
)

// Compile-time interface check.
var _ domain.Notifier = (*CLINotifier)(nil)

// ANSI escape codes for terminal formatting.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	red    = "\033[31m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
)

// PrintFunc is a function used to print formatted output.
// Matches the signature of both fmt.Printf and display.UI.Printf.
type PrintFunc func(format string, a ...interface{})

// CLINotifier writes notifications to stdout with ANSI formatting.
type CLINotifier struct {
	log     *logger.Logger
	printFn PrintFunc
}

// NewCLINotifier creates a stdout-based notifier.
// If printFn is nil, fmt.Printf is used.
func NewCLINotifier(log *logger.Logger, printFn PrintFunc) *CLINotifier {
	if printFn == nil {
		printFn = func(format string, a ...interface{}) {
			fmt.Printf(format+"\n", a...)
		}
	}
	return &CLINotifier{log: log, printFn: printFn}
}

// Notify prints a normal notification.
func (n *CLINotifier) Notify(ctx context.Context, message string) error {
	n.log.Debug("notify: %s", message)
	n.printFn("%s%s%s%s", cyan, bold, message, reset)
	return nil
}

// NotifyUrgent prints an urgent notification in bold red.
func (n *CLINotifier) NotifyUrgent(ctx context.Context, message string) error {
	n.log.Debug("notify-urgent: %s", message)
	n.printFn("%s%s%s%s", red, bold, message, reset)
	return nil
}
