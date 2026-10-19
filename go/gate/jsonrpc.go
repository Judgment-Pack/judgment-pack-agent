package gate

// jsonrpcMessage is the MCP SDK's JSONRPCMessageSchema (@modelcontextprotocol/sdk 1.30.0,
// types.js, Zod 4), the schema the TypeScript gate holds every outbound frame to: one of
//
//   - a request: exactly jsonrpc "2.0", an id, a string method and optional object params;
//   - a notification: exactly jsonrpc "2.0", a string method and optional object params;
//   - a result: exactly jsonrpc "2.0", an id and an object result;
//   - an error: exactly jsonrpc "2.0", an optional id and an error object with an integer code, a
//     string message and any data.
//
// An id is a string or a safe integer. params and result may carry other members; their _meta,
// where present, is an object whose progressToken is a string or a safe integer and whose
// io.modelcontextprotocol/related-task is an object with a string taskId.
func jsonrpcMessage(frame *jvalue) bool {
	return request(frame) || notification(frame) || resultResponse(frame) || errorResponse(frame)
}

// exactly reports an object whose members are among the named ones.
func exactly(v *jvalue, names ...string) bool {
	for _, key := range v.keys() {
		if !contains(names, key) {
			return false
		}
	}
	return true
}

func version(v *jvalue) bool {
	held := v.get("jsonrpc")
	return held != nil && held.kind == jstring && held.s == "2.0"
}

func requestID(v *jvalue) bool {
	return v != nil && (v.kind == jstring || safeInteger(v))
}

func object(v *jvalue) bool { return v != nil && v.kind == jobject }

// meta is RequestMetaSchema, a loose object.
func meta(v *jvalue) bool {
	if v == nil {
		return true
	}
	if !object(v) {
		return false
	}
	if token := v.get("progressToken"); token != nil && !(token.kind == jstring || safeInteger(token)) {
		return false
	}
	if related := v.get("io.modelcontextprotocol/related-task"); related != nil {
		if !object(related) {
			return false
		}
		if task := related.get("taskId"); task == nil || task.kind != jstring {
			return false
		}
	}
	return true
}

// params is a request's or notification's optional params: a loose object with an optional _meta.
func params(v *jvalue) bool {
	held := v.get("params")
	return held == nil || (object(held) && meta(held.get("_meta")))
}

func method(v *jvalue) bool {
	held := v.get("method")
	return held != nil && held.kind == jstring
}

func request(v *jvalue) bool {
	return exactly(v, "jsonrpc", "id", "method", "params") && version(v) && requestID(v.get("id")) && method(v) && params(v)
}

func notification(v *jvalue) bool {
	return exactly(v, "jsonrpc", "method", "params") && version(v) && method(v) && params(v)
}

func resultResponse(v *jvalue) bool {
	result := v.get("result")
	return exactly(v, "jsonrpc", "id", "result") && version(v) && requestID(v.get("id")) && object(result) && meta(result.get("_meta"))
}

func errorResponse(v *jvalue) bool {
	if !exactly(v, "jsonrpc", "id", "error") || !version(v) {
		return false
	}
	if id := v.get("id"); id != nil && !requestID(id) {
		return false
	}
	failure := v.get("error")
	if !object(failure) || !safeInteger(failure.get("code")) {
		return false
	}
	message := failure.get("message")
	return message != nil && message.kind == jstring
}
