package server

import (
	"github.com/kiwiirc/plugin-fileuploader/utils"
	"github.com/tus/tusd/v2/pkg/handler"
)

func (serv *UploadServer) preUploadCreate(hook handler.HookEvent) (handler.HTTPResponse, handler.FileInfoChanges, error) {
	response := handler.HTTPResponse{}

	fileID := utils.Uid()
	fileInfo := handler.FileInfoChanges{
		ID:       fileID,
		MetaData: hook.Upload.MetaData,
	}

	// Prevent identified pollution
	delete(fileInfo.MetaData, "identified")
	jwtAccount := hook.HTTPRequest.Header.Get("K-Jwt-Account")
	if jwtAccount != "" {
		fileInfo.MetaData["identified"] = "1"
	}

	if err := serv.DBConn.NewUpload(
		fileID,
		hook.HTTPRequest.Header.Get("K-Remote-IP"),
		hook.Upload.MetaData["filename"],
		hook.Upload.MetaData["filetype"],
		hook.HTTPRequest.Header.Get("K-Jwt-Nick"),
		jwtAccount,
		hook.HTTPRequest.Header.Get("K-Jwt-Issuer"),
	); err != nil {
		return handler.HTTPResponse{}, handler.FileInfoChanges{}, err
	}

	return response, fileInfo, nil
}

func (serv *UploadServer) preFinishResponse(hook handler.HookEvent) (handler.HTTPResponse, error) {
	response := handler.HTTPResponse{
		Header: map[string]string{
			"Upload-Metadata": handler.SerializeMetadataHeader(hook.Upload.MetaData),
		},
	}

	return response, nil
}
