package tools

import (
	"regexp"
	"strings"
	"time"
)

// freshRe is the Gate hint: a cheap, local check whether the latest user
// message looks like it needs current information. It only adds a sentence
// to the system prompt; the model still decides whether to call a tool.
var freshRe = regexp.MustCompile(`(?i)\b(today|tonight|tomorrow|yesterday|this (week|month|year)|latest|current(ly)?|right now|news|weather|forecast|price|stock|score|schedule|release date|who is the (current )?|20(2[5-9]|[3-9]\d))\b|오늘|내일|어제|이번\s*(주|달|해)|최신|현재|지금|뉴스|날씨|시세|주가|환율|가격|순위|결과|일정|출시|발표|대통령|총리|올해|최근`)

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
