package plugin

// mergePatternFields evaluates the plugin's host-side patterns and folds the
// captures into result.Fields. Conflict policy: a plugin's own field wins,
// because the plugin already had access to the same text and its value is more
// specific than a generic pattern.
//
// A required pattern that finds nothing surfaces as an error, so a
// misconfigured extractor fails loudly instead of emitting an empty field.
func mergePatternFields(result *Result, patterns []compiledPattern, msg *Mail) error {
	if len(patterns) == 0 {
		return nil
	}
	captured, err := applyPatterns(patterns, msg)
	if len(captured) > 0 {
		if result.Fields == nil {
			result.Fields = make(map[string]string, len(captured))
		}
		for k, v := range captured {
			if _, exists := result.Fields[k]; exists {
				continue
			}
			result.Fields[k] = v
		}
	}
	return err
}
