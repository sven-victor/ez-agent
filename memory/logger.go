package memory

import (
	"github.com/sven-victor/ez-agent/hook"
	"github.com/sven-victor/ez-agent/logger"
)

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

// SetHooks fills nil Hooks fields on known ContextPolicy trees (currently Summarize).
// Existing non-nil Hooks values are left unchanged. Nil hooks is ignored.
func SetHooks(p ContextPolicy, hooks *hook.Registry) {
	if p == nil || hooks == nil {
		return
	}
	switch v := p.(type) {
	case *Summarize:
		if v.Hooks == nil {
			v.Hooks = hooks
		}
		SetHooks(v.Inner, hooks)
		SetHooks(v.Fallback, hooks)
	case *Offload:
		SetHooks(v.Inner, hooks)
	case *DegradeMedia:
		SetHooks(v.Inner, hooks)
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
