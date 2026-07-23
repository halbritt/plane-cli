package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"plane-cli/internal/client"
	"plane-cli/internal/out"
)

func newAttachmentCmd(a *App) *cobra.Command {
	att := &cobra.Command{
		Use:   "attachment",
		Short: "Manage work item attachments (list/upload/download/get/delete)",
	}

	list := &cobra.Command{
		Use:   "list <issue>",
		Short: "List attachments on a work item",
		Long: `List uploaded attachments (plain array; the endpoint is not
paginated). Items carry metadata only — use "plane attachment get" for a
download URL.

Examples:
  plane attachment list DEPLOY-42`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runGet(cmd.Context(), cl, projPath(cfg, pid, "work-items", iid, "attachments"), nil)
		},
	}

	var upFile, upName, upType, upData string
	upload := &cobra.Command{
		Use:   "upload <issue>",
		Short: "Upload a file as a work item attachment",
		Long: `Upload a file end to end: request a presigned upload, POST the
file to storage, then confirm. The server enforces a MIME-type allow-list
and a size limit (default 5 MB) — larger files are rejected by storage.

--type is auto-detected from the file extension when omitted.

Examples:
  plane attachment upload DEPLOY-42 --file ./crash.log --type text/plain
  plane attachment upload DEPLOY-42 --file ./screenshot.png`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if upFile == "" {
				return a.usageErr("--file is required")
			}
			return a.attachmentUpload(cmd.Context(), args[0], upFile, upName, upType, upData)
		},
	}
	upload.Flags().StringVar(&upFile, "file", "", "path of the file to upload (required)")
	upload.Flags().StringVar(&upName, "name", "", "attachment name (default: file basename)")
	upload.Flags().StringVar(&upType, "type", "", "MIME type (default: detected from extension)")
	upload.Flags().StringVar(&upData, "data", "", "extra fields for the create call (e.g. external_id)")

	get := &cobra.Command{
		Use:   "get <issue> <attachment-id>",
		Short: "Get a presigned download URL for an attachment",
		Long: `Return {"download_url": ...} for an attachment. The URL is
presigned and expires (default 1h); fetch it without the API key.

Examples:
  plane attachment get DEPLOY-42 8d3e...a7`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			loc, err := cl.Location(cmd.Context(), projPath(cfg, pid, "work-items", iid, "attachments", args[1]))
			if err != nil {
				return a.fail(err)
			}
			return a.success(map[string]string{"download_url": loc}, nil)
		},
	}

	var dlOutput string
	download := &cobra.Command{
		Use:   "download <issue> <attachment-id>",
		Short: "Download an attachment to a file",
		Long: `Download an attachment's bytes to a local file. Without
--output the attachment's own name is used (in the current directory).
Binary content never goes to stdout — the envelope reports the saved path.

Examples:
  plane attachment download DEPLOY-42 8d3e...a7 --output /tmp/crash.log
  plane attachment download DEPLOY-42 8d3e...a7`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.attachmentDownload(cmd.Context(), args[0], args[1], dlOutput)
		},
	}
	download.Flags().StringVar(&dlOutput, "output", "", "destination path (default: attachment's stored name)")

	del := &cobra.Command{
		Use:   "delete <issue> <attachment-id>",
		Short: "Delete an attachment",
		Long: `Delete (soft-delete) an attachment by UUID.

Examples:
  plane attachment delete DEPLOY-42 8d3e...a7`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cl, cfg, pid, iid, err := a.issueScope(cmd.Context(), args[0])
			if err != nil {
				return a.fail(err)
			}
			return a.runMutate(cmd.Context(), cl, http.MethodDelete,
				projPath(cfg, pid, "work-items", iid, "attachments", args[1]), nil)
		},
	}

	att.AddCommand(list, upload, get, download, del)
	return att
}

// uploadTicket is the response of POST .../attachments/ (presign step).
type uploadTicket struct {
	UploadData struct {
		URL    string            `json:"url"`
		Fields map[string]string `json:"fields"`
	} `json:"upload_data"`
	AssetID    string          `json:"asset_id"`
	Attachment json.RawMessage `json:"attachment"`
}

func (a *App) attachmentUpload(ctx context.Context, issueRef, file, name, mimeType, data string) error {
	cl, cfg, pid, iid, err := a.issueScope(ctx, issueRef)
	if err != nil {
		return a.fail(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		return a.fail(&usageError{msg: fmt.Sprintf("cannot stat --file: %v", err)})
	}
	if name == "" {
		name = filepath.Base(file)
	}
	if mimeType == "" {
		mimeType = mime.TypeByExtension(filepath.Ext(file))
		if i := strings.IndexByte(mimeType, ';'); i > 0 {
			mimeType = mimeType[:i]
		}
		if mimeType == "" {
			return a.fail(&usageError{msg: "cannot detect MIME type from extension; pass --type (must be on the server's allow-list)"})
		}
	}

	// Step 1: create the asset and get the presigned POST policy.
	fields := map[string]any{"name": name, "size": info.Size(), "type": mimeType}
	body, err := payload(data, fields)
	if err != nil {
		return a.fail(err)
	}
	base := projPath(cfg, pid, "work-items", iid, "attachments")
	resp, err := cl.Do(ctx, http.MethodPost, base, nil, body)
	if err != nil {
		return a.fail(err)
	}
	var ticket uploadTicket
	if err := json.Unmarshal(resp.Body, &ticket); err != nil || ticket.AssetID == "" || ticket.UploadData.URL == "" {
		return a.fail(fmt.Errorf("unexpected presign response: %s", truncateForErr(resp.Body)))
	}

	// Step 2: multipart POST to storage (no API key; the policy authorizes).
	if err := a.postToStorage(ctx, cl, cfg.BaseURL, &ticket, file); err != nil {
		return a.fail(err)
	}

	// Step 3: confirm the upload (body is ignored by the server).
	if _, err := cl.Do(ctx, http.MethodPatch, base+ticket.AssetID+"/", nil, map[string]any{"is_uploaded": true}); err != nil {
		return a.fail(err)
	}

	return a.success(map[string]any{
		"asset_id":   ticket.AssetID,
		"attachment": ticket.Attachment,
		"uploaded":   true,
	}, out.Meta{"file": file, "size": info.Size(), "type": mimeType})
}

// postToStorage performs the S3 POST-policy upload: every policy field, then
// the file part last, as S3-compatible stores require.
func (a *App) postToStorage(ctx context.Context, cl *client.Client, baseURL string, ticket *uploadTicket, file string) error {
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range ticket.UploadData.Fields {
		if err := mw.WriteField(k, v); err != nil {
			return err
		}
	}
	fw, err := mw.CreateFormFile("file", filepath.Base(file))
	if err != nil {
		return err
	}
	if _, err := io.Copy(fw, f); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}

	uploadURL := ticket.UploadData.URL
	if strings.HasPrefix(uploadURL, "/") {
		uploadURL = strings.TrimRight(baseURL, "/") + uploadURL
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if a.flagDebug {
		fmt.Fprintf(a.Stderr, "[debug] > POST %s (storage upload, %d policy fields)\n", uploadURL, len(ticket.UploadData.Fields))
	}
	resp, err := cl.HTTP.Do(req)
	if err != nil {
		return &client.NetError{Err: err}
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if a.flagDebug {
		fmt.Fprintf(a.Stderr, "[debug] < HTTP %d (storage)\n", resp.StatusCode)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("storage upload failed: HTTP %d: %s", resp.StatusCode, truncateForErr(respBody))
	}
	return nil
}

func (a *App) attachmentDownload(ctx context.Context, issueRef, assetID, output string) error {
	cl, cfg, pid, iid, err := a.issueScope(ctx, issueRef)
	if err != nil {
		return a.fail(err)
	}
	base := projPath(cfg, pid, "work-items", iid, "attachments")

	if output == "" {
		// Find the attachment's stored name in the (unpaginated) list.
		resp, err := cl.Do(ctx, http.MethodGet, base, nil, nil)
		if err != nil {
			return a.fail(err)
		}
		var items []struct {
			ID         string `json:"id"`
			Attributes struct {
				Name string `json:"name"`
			} `json:"attributes"`
		}
		if err := json.Unmarshal(resp.Body, &items); err == nil {
			for _, it := range items {
				if it.ID == assetID && it.Attributes.Name != "" {
					output = filepath.Base(it.Attributes.Name)
					break
				}
			}
		}
		if output == "" {
			output = assetID
		}
	}

	loc, err := cl.Location(ctx, base+assetID+"/")
	if err != nil {
		return a.fail(err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, loc, nil)
	if err != nil {
		return a.fail(err)
	}
	resp, err := cl.HTTP.Do(req)
	if err != nil {
		return a.fail(&client.NetError{Err: err})
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return a.fail(fmt.Errorf("storage download failed: HTTP %d: %s", resp.StatusCode, truncateForErr(b)))
	}
	out_, err := os.Create(output)
	if err != nil {
		return a.fail(err)
	}
	n, err := io.Copy(out_, resp.Body)
	if cerr := out_.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return a.fail(fmt.Errorf("writing %s: %w", output, err))
	}
	return a.success(map[string]any{"saved_to": output, "bytes": n}, nil)
}

func truncateForErr(b []byte) string {
	s := string(b)
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
