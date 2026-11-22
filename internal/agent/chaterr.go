package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// ChatFail is why an overlay chat got no answer.
type ChatFail string

// Chat failure kinds. The llm_* reasons come from the server's chat endpoint.
const (
	FailTimeout  ChatFail = "timeout"     // server or LLM too slow
	FailOffline  ChatFail = "offline"     // cannot reach the server at all
	FailCap      ChatFail = "llm_cap"     // daily LLM quota used up
	FailLLM      ChatFail = "llm"         // every model failed (other reasons)
	FailBlocked  ChatFail = "llm_blocked" // gateway refused the models: configuration
	FailQuota    ChatFail = "llm_quota"   // provider quota / rate limit
	FailDown     ChatFail = "llm_down"    // provider down
	FailDisabled ChatFail = "disabled"    // LLM not configured on the server
	FailAuth     ChatFail = "auth"        // device token rejected
	FailBusy     ChatFail = "busy"        // rate limited
	FailServer   ChatFail = "server"      // any other server error
)

// ClassifyChatError maps a Client.Chat error to a ChatFail.
func ClassifyChatError(err error) ChatFail {
	var se *StatusError
	if errors.As(err, &se) {
		switch se.Reason {
		case "llm_cap":
			return FailCap
		case "llm_timeout":
			return FailTimeout
		case "llm_disabled":
			return FailDisabled
		case "llm_failed":
			return FailLLM
		case "llm_blocked":
			return FailBlocked
		case "llm_quota":
			return FailQuota
		case "llm_down":
			return FailDown
		}
		switch se.Code {
		case http.StatusUnauthorized, http.StatusForbidden:
			return FailAuth
		case http.StatusTooManyRequests:
			return FailBusy
		case http.StatusBadGateway, http.StatusServiceUnavailable:
			return FailOffline // reverse proxy up, Tunas server down
		case http.StatusGatewayTimeout:
			return FailTimeout
		}
		return FailServer
	}
	// connection problems first: a dial that times out is still "offline"
	var dns *net.DNSError
	var op *net.OpError
	if errors.As(err, &dns) || (errors.As(err, &op) && op.Op == "dial") {
		return FailOffline
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return FailTimeout
	}
	return FailOffline
}
