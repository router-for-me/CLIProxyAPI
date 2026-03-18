package cmd

func selectOAuthCallbackPrompt(options *LoginOptions, promptFn func(string) (string, error)) func(string) (string, error) {
	if options == nil || !options.NoBrowser {
		return nil
	}
	return promptFn
}
