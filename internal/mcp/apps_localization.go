package mcp

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/uvwt/agentdock-protocol/mcpapps"
)

// Extend the pinned protocol renderer's existing message dictionary, not its
// protocol or data model. Dependency asset changes must fail the contract tests
// rather than silently shipping an incomplete Korean interface.
//
//go:embed apps_ko-KR.json
var koreanAppMessages []byte

var koreanAppMessagesJS = func() string {
	var messages map[string]string
	if err := json.Unmarshal(koreanAppMessages, &messages); err != nil {
		panic(err)
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}()

func localizedAppHTML(view, title string) string {
	page := mcpapps.HTML(view, title)
	replace := func(old, new string) {
		if strings.Count(page, old) != 1 {
			panic(fmt.Sprintf("pinned MCP App localization anchor changed: %q", old))
		}
		page = strings.Replace(page, old, new, 1)
	}
	replace("  const matchLocale=value=>{", "  messages[\"ko-KR\"]="+koreanAppMessagesJS+";\n"+
		"  Object.assign(messages.en,{role_user:\"user\",role_assistant:\"assistant\",acpAgent:\"ACP agent\"});\n"+
		"  Object.assign(messages[\"zh-CN\"],{role_user:\"user\",role_assistant:\"assistant\",acpAgent:\"ACP agent\"});\n"+
		"  const matchLocale=value=>{")
	replace(`    if(candidate==="en"||candidate.startsWith("en-"))return "en";`,
		"    if(candidate===\"ko\"||candidate.startsWith(\"ko-\"))return \"ko-KR\";\n"+
			`    if(candidate==="en"||candidate.startsWith("en-"))return "en";`)
	replace(`  const applyLocale=value=>{const next=resolveLocale([value]);if(locale===next)return;locale=next;document.documentElement.lang=locale;if(lastData){lastSerialized="";render(lastData)}};`,
		`  const originalDocumentTitle=document.title;
  const refreshLocalePresentation=()=>{document.documentElement.lang=locale;document.title=messageFor("title_"+expectedView)??originalDocumentTitle;if(!lastData)root.textContent=t("waiting")};
  const applyLocale=value=>{const next=resolveLocale([value]);if(locale===next)return;locale=next;refreshLocalePresentation();if(lastData){lastSerialized="";render(lastData)}};`)
	replace("  document.documentElement.lang=locale;\n", "  refreshLocalePresentation();\n")
	replace(`:first?(first.status||""):"";`, `:first?stateLabel(first.status):"";`)
	replace(`const listMeta=first?[first.status||"",first.agent||"",first.cwd||""]`, `const listMeta=first?[stateLabel(first.status),first.agent||"",first.cwd||""]`)
	replace(`const sessionMeta=[session.status||state.status,session.agent||"",session.cwd||""]`, `const sessionMeta=[stateLabel(session.status||state.status),session.agent||"",session.cwd||""]`)
	replace(`el("span","compact-meta-row",session.status||state.status)`, `el("span","compact-meta-row",stateLabel(session.status||state.status))`)
	replace(`el("div","message-role",message.role)`, `el("div","message-role",t("role_"+message.role))`)
	replace(`data.acp.description||"ACP agent"`, `data.acp.description||t("acpAgent")`)
	return page
}
