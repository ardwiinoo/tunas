package projects

// TitleReader reads the foreground window of the user. The title it returns
// must only be handed to Extractor.Project and dropped (faked in tests; the
// Windows implementation is the only place that calls the title API).
type TitleReader interface {
	// Foreground returns the executable name and the window title of the
	// foreground window ("" when there is none).
	Foreground() (process, title string, err error)
}

// ReaderFunc adapts a function to TitleReader.
type ReaderFunc func() (string, string, error)

// Foreground implements TitleReader.
func (f ReaderFunc) Foreground() (string, string, error) { return f() }
