package tools

import (
	"regexp"
	"strings"
	"time"
)

// freshRe is the Gate hint (design §4.3): a cheap, local check whether the
// latest user message looks like it needs current information. The vocabulary
// is the one list the design names - today, yesterday, now, recently, these
// days, this week, latest, current, market price, weather, news, tidings,
// real time, this year, 20xx - and nothing broader, so the hint stays a
// positive signal with few false positives. It only adds a sentence to the
// system prompt; the model still decides whether to call a tool, and a fresh
// hint with no tool call is reported as fresh:"unverified".
var freshRe = regexp.MustCompile(`(?i)\b(today|tonight|yesterday|right now|these days|recent(ly)?|this week|latest|current(ly)?|weather|news|real[- ]?time|this year|20(2[5-9]|[3-9]\d))\b|오늘|어제|지금|최근|요즘|이번\s*주|최신|현재|시세|날씨|뉴스|소식|실시간|올해|20(2[5-9]|[3-9]\d)\s*년`)

// Gate reports whether text looks time-sensitive.
func Gate(text string) bool { return freshRe.MatchString(strings.TrimSpace(text)) }

// Prompt returns the fixed gateway system text for an auto-selection turn:
// the tool rules, today's date in Asia/Seoul, and (when fresh) a hint.
func Prompt(now time.Time, fresh bool) string {
	d := now.In(kst)
	s := SystemPrompt + "\nToday is " + d.Format("2006-01-02 (Mon)") + " in Asia/Seoul (UTC+9). Your training data may be out of date."
	if fresh {
		s += "\nThe latest user message may depend on current information: prefer calling web_search before answering."
	}
	return s
}

// UnverifiedNotice is appended (gateway text) when current information was
// needed but no tool call produced a usable result.
const UnverifiedNotice = `The web tools have not returned a usable result so far. If you answer without usable sources, begin your answer by saying in one short sentence that you could not verify current information, then answer from your training data and say it may be out of date.`
