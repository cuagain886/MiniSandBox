package logging

import "context"

type contextKey struct{}

type operationContext struct {
	component   SafeValue
	requestID   SafeID
	sandboxID   SafeID
	executionID SafeID
}

// WithComponent 返回携带稳定模块名的 child context；空或非法零值保持原 context。
func WithComponent(ctx context.Context, component SafeValue) context.Context {
	if ctx == nil || component.value == "" {
		return ctx
	}
	fields := fieldsFromContext(ctx)
	fields.component = component
	return context.WithValue(ctx, contextKey{}, fields)
}

// WithRequestID 返回覆盖 request ID 的 child context；只接受 request kind。
func WithRequestID(ctx context.Context, id SafeID) context.Context {
	if ctx == nil || id.kind != IDKindRequest || id.value == "" {
		return ctx
	}
	fields := fieldsFromContext(ctx)
	fields.requestID = id
	return context.WithValue(ctx, contextKey{}, fields)
}

// WithSandboxID 返回覆盖 sandbox ID 的 child context；不会改变父 context。
func WithSandboxID(ctx context.Context, id SafeID) context.Context {
	if ctx == nil || id.kind != IDKindSandbox || id.value == "" {
		return ctx
	}
	fields := fieldsFromContext(ctx)
	fields.sandboxID = id
	return context.WithValue(ctx, contextKey{}, fields)
}

// WithExecutionID 返回覆盖 execution ID 的 child context；不会改变父 context。
func WithExecutionID(ctx context.Context, id SafeID) context.Context {
	if ctx == nil || id.kind != IDKindExecution || id.value == "" {
		return ctx
	}
	fields := fieldsFromContext(ctx)
	fields.executionID = id
	return context.WithValue(ctx, contextKey{}, fields)
}

// ContextAttrs 只投影本包定义的四类字段，不枚举或序列化其他 context value。
func ContextAttrs(ctx context.Context) []Attr {
	fields := fieldsFromContext(ctx)
	result := make([]Attr, 0, 4)
	if fields.component.value != "" {
		attr, _ := ValueAttr(FieldComponent, fields.component)
		result = append(result, attr)
	}
	for _, item := range []struct {
		field Field
		id    SafeID
	}{{FieldRequestID, fields.requestID}, {FieldSandboxID, fields.sandboxID}, {FieldExecutionID, fields.executionID}} {
		if item.id.value != "" {
			attr, _ := IDAttr(item.field, item.id)
			result = append(result, attr)
		}
	}
	return result
}

// RequestIDFromContext 返回 middleware 已验证的 request ID；缺失时返回 false。
func RequestIDFromContext(ctx context.Context) (SafeID, bool) {
	id := fieldsFromContext(ctx).requestID
	return id, id.kind == IDKindRequest && id.value != ""
}

func fieldsFromContext(ctx context.Context) operationContext {
	if ctx == nil {
		return operationContext{}
	}
	fields, _ := ctx.Value(contextKey{}).(operationContext)
	return fields
}
