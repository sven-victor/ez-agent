package memory

import "github.com/sven-victor/ez-agent/logger"

// SetLogger fills nil Logger fields on known ContextPolicy / Summarizer trees.
// Existing non-nil Logger values are left unchanged. Nil log is ignored.
func SetLogger(p ContextPolicy, log logger.Logger) {
	if p == nil || log == nil {
		return
	}
	switch v := p.(type) {
	case *Summarize:
		if v.Logger == nil {
			v.Logger = log
		}
		setSummarizerLogger(v.Summarizer, log)
		setSummarizerLogger(v.Segmented, log)
		SetLogger(v.Inner, log)
		SetLogger(v.Fallback, log)
	case *Window:
		if v.Logger == nil {
			v.Logger = log
		}
	case *Offload:
		if v.Logger == nil {
			v.Logger = log
		}
		SetLogger(v.Inner, log)
	case *DegradeMedia:
		if v.Logger == nil {
			v.Logger = log
		}
		SetLogger(v.Inner, log)
	}
}

func setSummarizerLogger(s Summarizer, log logger.Logger) {
	if s == nil || log == nil {
		return
	}
	switch v := s.(type) {
	case *RobustSummarizer:
		if v.Logger == nil {
			v.Logger = log
		}
	case *SegmentedSummarizer:
		if v.Logger == nil {
			v.Logger = log
		}
	case *ProviderSummarizer:
		if v.Logger == nil {
			v.Logger = log
		}
	}
}

func policyLog(l logger.Logger) logger.Logger {
	return logger.OrNop(l)
}
