package server

import "errors"

var (
	errProviderAlreadyExists  = errors.New("provider already exists")
	errProviderNotFound       = errors.New("provider not found")
	errProviderNoCustomConfig = errors.New("provider has no custom settings")
	errModelAlreadyExists     = errors.New("model already exists")
	errModelNotFound          = errors.New("model not found")
	errSelectedModelNoImage   = errors.New("selected model does not support image input")
	errAttachmentPathRequired = errors.New("attachment path required")
	errAttachmentPathInvalid  = errors.New("invalid attachment path")
	errAttachmentUnreadable   = errors.New("attachment unreadable")
	errImageTooLarge          = errors.New("image exceeds 20 MiB")
	errUnsupportedContent     = errors.New("unsupported type")
	errTextOrAttachment       = errors.New("text or attachment required")
	errModelUnavailable       = errors.New("unavailable")
	errQueueIDRequiresSteer   = errors.New("queueId requires delivery=steer")
	errQueueIDWithContent     = errors.New("queueId cannot include content")
	errQueueIDWithParent      = errors.New("queueId cannot be used with parentId")
)
