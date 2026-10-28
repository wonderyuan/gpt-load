package control

import (
	"context"

	"github.com/gin-gonic/gin"

	"gpt-load/internal/modelinfo"
	app_errors "gpt-load/internal/platform/errors"
	"gpt-load/internal/platform/response"
)

type ModelInfoReader interface {
	Read(context.Context) (modelinfo.Report, error)
}

func (s *Server) SetModelInfoReader(reader ModelInfoReader) {
	s.modelInfo = reader
}

func (s *Server) handleForkModelInfo(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if c.Request.URL.RawQuery != "" || c.Request.URL.ForceQuery {
		writeServiceError(c, "model_info", app_errors.ErrBadRequest)
		return
	}
	if s.modelInfo == nil {
		writeServiceError(c, "model_info", app_errors.ErrInternalServer)
		return
	}
	report, err := s.modelInfo.Read(c.Request.Context())
	if err != nil {
		writeServiceError(c, "model_info", err)
		return
	}
	response.SuccessI18n(c, "common.success", report)
}
