package bite

import (
	"fmt"
	"io"
	"math/rand"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/landoop/tableprinter"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

type HelpTemplate struct {
	BuildTime            string
	BuildRevision        string
	ShowGoRuntimeVersion bool

	Template fmt.Stringer
}

func (h HelpTemplate) String() string {
	if tmpl := h.Template.String(); tmpl != "" {
		return tmpl
	}
	buildTitle := ">>>> build" // if we ever want an emoji, there is one: \U0001f4bb
	tab := strings.Repeat(" ", len(buildTitle))

	// unix nanoseconds, as int64, to a human readable time, defaults to time.UnixDate, i.e:
	// Thu Mar 22 02:40:53 UTC 2018
	// but can be changed to something like "Mon, 01 Jan 2006 15:04:05 GMT" if needed.
	n, _ := strconv.ParseInt(h.BuildTime, 10, 64)
	buildTimeStr := time.Unix(n, 0).Format(time.UnixDate)

	tmpl := `{{with .Name}}{{printf "%s " .}}{{end}}{{printf "version %s" .Version}}` +
		fmt.Sprintf("\n%s\n", buildTitle) +
		fmt.Sprintf("%s revision %s\n", tab, h.BuildRevision) +
		fmt.Sprintf("%s datetime %s\n", tab, buildTimeStr)
	if h.ShowGoRuntimeVersion {
		tmpl += fmt.Sprintf("%s go       %s\n", tab, runtime.Version())
	}

	return tmpl
}

type Application struct {
	Name        string
	Version     string
	Description string
	Long        string

	HelpTemplate fmt.Stringer
	// ShowSpinner if true(default is false) and machine-friendly is false(default is true) then
	// it waits via "visual" spinning before each command's job done.
	ShowSpinner bool
	// if true then the --machine-friendly flag will be added to the application and PrintObject will check for that.
	DisableOutputFormatController bool
	MachineFriendly               *bool
	PersistentFlags               func(*pflag.FlagSet)

	Setup          CobraRunner
	Shutdown       CobraRunner
	commands       []*cobra.Command // commands should be builded and added on "Build" state or even after it, `AddCommand` will handle this.
	currentCommand *cobra.Command

	FriendlyErrors FriendlyErrors
	Memory         *Memory

	CobraCommand *cobra.Command // the root command, after "Build" state.
}

func (app *Application) Print(format string, args ...interface{}) error {
	if !strings.HasSuffix(format, "\n") {
		format += "\r\n" // add a new line.
	}

	_, err := fmt.Fprintf(app, format, args...)
	return err
}

func (app *Application) PrintInfo(format string, args ...interface{}) error {
	if *app.MachineFriendly || GetSilentFlag(app.currentCommand) {
		// check both --machine-friendly and --silent(optional flag,
		// but can be used side by side without machine friendly to disable info messages on user-friendly state)
		return nil
	}

	return app.Print(format, args...)
}

func (app *Application) PrintObject(v interface{}) error {
	return PrintObject(app.currentCommand, v)
}

// func (app *Application) writeObject(out io.Writer, v interface{}, tableOnlyFilters ...interface{}) error {
// 	machineFriendlyFlagValue := GetMachineFriendlyFlag(app.CobraCommand)
// 	if machineFriendlyFlagValue {
// 		prettyFlagValue := !GetJSONNoPrettyFlag(app.currentCommand)
// 		jmesQueryPathFlagValue := GetJSONQueryFlag(app.currentCommand)
// 		return WriteJSON(out, v, prettyFlagValue, jmesQueryPathFlagValue)
// 	}
//
// 	tableprinter.Print(out, v, tableOnlyFilters...)
// 	return nil
// }

func PrintObject(cmd *cobra.Command, v interface{}, tableOnlyFilters ...interface{}) error {
	out := cmd.Root().OutOrStdout()
	machineFriendlyFlagValue := GetMachineFriendlyFlag(cmd)
	if machineFriendlyFlagValue {
		prettyFlagValue := !GetJSONNoPrettyFlag(cmd)
		jmesQueryPathFlagValue := GetJSONQueryFlag(cmd)
		return WriteJSON(out, v, prettyFlagValue, jmesQueryPathFlagValue)
	}

	tableprinter.Print(out, v, tableOnlyFilters...)
	return nil
}

func (app *Application) Write(b []byte) (int, error) {
	if app.CobraCommand == nil {
		return os.Stdout.Write(b)
	}

	return app.CobraCommand.OutOrStdout().Write(b)
}

func (app *Application) AddCommand(cmd *cobra.Command) {
	if app.CobraCommand == nil {
		// not builded yet, add these commands.
		app.commands = append(app.commands, cmd)
	} else {
		// builded, add them directly as cobra commands.
		app.CobraCommand.AddCommand(cmd)
	}
}

func (app *Application) Run(output io.Writer, args []string) error {
	rand.Seed(time.Now().UTC().UnixNano()) // <3

	if output == nil {
		output = os.Stdout
	}

	rootCmd := Build(app)
	rootCmd.SetOutput(output)
	if len(args) == 0 && len(os.Args) > 0 {
		args = os.Args[1:]
	}

	if !rootCmd.DisableFlagParsing {
		rootCmd.ParseFlags(args)
	}

	app.commands = nil

	if app.ShowSpinner && !*app.MachineFriendly {
		return ackError(app.FriendlyErrors, ExecuteWithSpinner(rootCmd))
	}

	return ackError(app.FriendlyErrors, rootCmd.Execute())
}

func (app *Application) exampleText(str string) string {
	return fmt.Sprintf("%s %s", app.Name, str)
}

// keeps track of the Applications, this is the place that built applications are being stored,
// so the `Get` can receive the exact Application that the command belongs to, a good example is the `FriendlyError` function and all the `bite` package-level helpers.
var applications []*Application

func registerApplication(app *Application) {
	for i, a := range applications {
		if a.Name == app.Name {
			// override the existing and exit.
			applications[i] = app
			return
		}
	}

	applications = append(applications, app)
}

func Get(cmd *cobra.Command) *Application {
	if app := GetByName(cmd.Name()); app != nil {
		return app
	}

	if cmd.HasParent() {
		return Get(cmd.Parent())
	}

	return nil
}

func GetByName(applicationName string) *Application {
	for _, app := range applications {
		if app.Name == applicationName {
			return app
		}
	}

	return nil
}

func FindCommand(applicationName string, args []string) (*cobra.Command, []string) {
	app := GetByName(applicationName)
	if app == nil {
		return nil, nil
	}

	c, cArgs, err := app.CobraCommand.Find(args)
	if err != nil {
		return nil, nil
	}

	return c, cArgs
}

func (app *Application) FindCommand(args []string) (*cobra.Command, []string) {
	return FindCommand(app.Name, args)
}

func getCommand(from *cobra.Command, subCommandName string) *cobra.Command {
	for _, c := range from.Commands() {
		if c.Name() == subCommandName {
			return c
		}

		return getCommand(c, subCommandName)
	}

	return nil
}

func GetCommand(applicationName string, commandName string) *cobra.Command {
	app := GetByName(applicationName)
	if app == nil {
		return nil
	}

	return getCommand(app.CobraCommand, commandName)
}

func (app *Application) GetCommand(commandName string) *cobra.Command {
	return GetCommand(app.Name, commandName)
}

func Build(app *Application) *cobra.Command {
	if app.CobraCommand != nil {
		return app.CobraCommand
	}

	if app.FriendlyErrors == nil {
		app.FriendlyErrors = FriendlyErrors{}
	}

	if app.Memory == nil {
		app.Memory = makeMemory()
	}

	useText := app.Name
	if strings.LastIndexByte(app.Name, '[') < len(strings.Split(app.Name, " ")[0]) {
		useText = fmt.Sprintf("%s [command] [flags]", app.Name)
	}

	if app.Long == "" {
		app.Long = app.Description
	}

	rootCmd := &cobra.Command{
		Version:                    app.Version,
		Use:                        useText,
		Short:                      app.Description,
		Long:                       app.Long,
		SilenceUsage:               true,
		SilenceErrors:              true,
		TraverseChildren:           true,
		SuggestionsMinimumDistance: 1,
	}

	app.MachineFriendly = new(bool)
	if !app.DisableOutputFormatController {
		RegisterMachineFriendlyFlagTo(rootCmd.PersistentFlags(), app.MachineFriendly)
	}

	fs := rootCmd.PersistentFlags()
	if app.PersistentFlags != nil {
		app.PersistentFlags(fs)
	}

	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		app.currentCommand = cmd // bind current command here.

		if app.Setup != nil {
			return app.Setup(cmd, args)
		}

		return nil
	}

	rootCmd.PersistentPostRunE = func(cmd *cobra.Command, args []string) error {
		if app.Shutdown != nil {
			return app.Shutdown(cmd, args)
		}

		return nil
	}

	if len(app.commands) > 0 {
		for _, cmd := range app.commands {
			rootCmd.AddCommand(cmd)
		}

		// clear mem.
		app.commands = nil
	}

	if rootCmd.HasAvailableSubCommands() {
		exampleText := rootCmd.Commands()[0].Example
		rootCmd.Example = exampleText
	}

	if app.HelpTemplate != nil {
		if helpTmpl := app.HelpTemplate.String(); helpTmpl != "" {
			rootCmd.SetVersionTemplate(helpTmpl)
		}
	}

	app.currentCommand = rootCmd
	app.CobraCommand = rootCmd

	registerApplication(app)
	return rootCmd
}

const machineFriendlyFlagKey = "machine-friendly"

func GetMachineFriendlyFlagFrom(set *pflag.FlagSet) bool {
	b, _ := set.GetBool(machineFriendlyFlagKey)
	return b
}

func GetMachineFriendlyFlag(cmd *cobra.Command) bool {
	return GetMachineFriendlyFlagFrom(cmd.Flags())
}

func RegisterMachineFriendlyFlagTo(set *pflag.FlagSet, ptr *bool) {
	if !GetMachineFriendlyFlagFrom(set) {
		if ptr == nil {
			ptr = new(bool)
		}
		set.BoolVar(ptr, machineFriendlyFlagKey, false, "--"+machineFriendlyFlagKey+" to output JSON results and hide all the info messages")
	}
}

func RegisterMachineFriendlyFlag(cmd *cobra.Command, ptr *bool) {
	RegisterMachineFriendlyFlagTo(cmd.Flags(), ptr)
}

type ApplicationBuilder struct {
	app *Application
}

func Name(name string) *ApplicationBuilder {
	return &ApplicationBuilder{
		app: &Application{Name: name},
	}
}

func (b *ApplicationBuilder) Get() *Application {
	return b.app
}

func (b *ApplicationBuilder) Description(description string) *ApplicationBuilder {
	b.app.Description = description
	return b
}

func (b *ApplicationBuilder) Version(version string) *ApplicationBuilder {
	b.app.Version = version
	return b
}

func (b *ApplicationBuilder) Setup(setupFunc CobraRunner) *ApplicationBuilder {
	b.app.Setup = setupFunc
	return b
}

func (b *ApplicationBuilder) Flags(fn func(*Flags)) *ApplicationBuilder {
	b.app.PersistentFlags = fn
	return b
}

func (b *ApplicationBuilder) GetFlags() *Flags {
	return b.app.CobraCommand.Flags()
}

func (b *ApplicationBuilder) Parse(args ...string) error {
	rootCmd := Build(b.app)
	return rootCmd.ParseFlags(args)
}

func (b *ApplicationBuilder) Run(w io.Writer, args []string) error {
	return b.app.Run(w, args)
}
