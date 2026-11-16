package panel

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed i18n/*.json
var i18nFS embed.FS

var messages = map[string]map[string]string{}

var Locales = []string{"zh-CN", "zh-TW", "en"}

func init() {
	for _, l := range Locales {
		b, err := i18nFS.ReadFile("i18n/" + l + ".json")
		if err != nil {
			panic(err)
		}
		m := map[string]string{}
		if err := json.Unmarshal(b, &m); err != nil {
			panic(fmt.Sprintf("i18n/%s.json: %v", l, err))
		}
		messages[l] = m
	}
}

// T translates code into locale, filling {name} placeholders from params.
// Falls back to English, then to the code itself.
func T(locale, code string, params map[string]any) string {
	msg, ok := messages[locale][code]
	if !ok {
		if msg, ok = messages["en"][code]; !ok {
			msg = code
		}
	}
	for k, v := range params {
		msg = strings.ReplaceAll(msg, "{"+k+"}", fmt.Sprint(v))
	}
	return msg
}

// pickLocale maps an Accept-Language header (or a single tag) to a supported locale.
func pickLocale(accept string) string {
	for _, part := range strings.Split(accept, ",") {
		tag := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch {
		case tag == "":
			continue
		case strings.HasPrefix(tag, "zh-tw"), strings.HasPrefix(tag, "zh-hk"), strings.HasPrefix(tag, "zh-mo"), strings.HasPrefix(tag, "zh-hant"):
			return "zh-TW"
		case strings.HasPrefix(tag, "zh"):
			return "zh-CN"
		case strings.HasPrefix(tag, "en"):
			return "en"
		}
	}
	return "zh-CN"
}
