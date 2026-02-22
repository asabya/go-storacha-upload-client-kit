package go_storacha_upload_client_kit

import (
	"context"
	"fmt"

	uploadcap "github.com/storacha/go-libstoracha/capabilities/upload"
	"github.com/storacha/go-ucanto/core/result"
	"github.com/storacha/go-ucanto/did"
)

// UploadList returns a paginated list of uploads in a space.
//
// Required delegated capability proofs: `upload/list`
//
// The `space` is the DID of the space to list uploads from.
// The `params` carry optional pagination cursor via uploadcap.ListCaveats{Cursor: cursor}.
//
// Callers should loop, passing the returned Cursor back until it is nil:
//
//	var cursor *string
//	for {
//	    ok, err := client.UploadList(ctx, space, uploadcap.ListCaveats{Cursor: cursor})
//	    if err != nil { ... }
//	    for _, r := range ok.Results { fmt.Println(r.Root) }
//	    if ok.Cursor == nil { break }
//	    cursor = ok.Cursor
//	}
func (c *StorachaClient) UploadList(ctx context.Context, space did.DID, params uploadcap.ListCaveats) (uploadcap.ListOk, error) {
	res, _, err := invokeAndExecute[uploadcap.ListCaveats, uploadcap.ListOk](
		ctx,
		c,
		uploadcap.List,
		space.String(),
		params,
		uploadcap.ListOkType(),
	)
	if err != nil {
		return uploadcap.ListOk{}, fmt.Errorf("invoking and executing `upload/list`: %w", err)
	}

	listOk, failErr := result.Unwrap(res)
	if failErr != nil {
		return uploadcap.ListOk{}, fmt.Errorf("`upload/list` failed: %w", failErr)
	}

	return listOk, nil
}
