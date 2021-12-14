package api

import (
	"net/http"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"github.com/photoprism/photoprism/internal/acl"
	"github.com/photoprism/photoprism/internal/entity"
	"github.com/photoprism/photoprism/internal/event"
	"github.com/photoprism/photoprism/internal/form"
	"github.com/photoprism/photoprism/internal/i18n"
	"github.com/photoprism/photoprism/internal/photoprism"
	"github.com/photoprism/photoprism/internal/query"
	"github.com/photoprism/photoprism/internal/service"

	"github.com/photoprism/photoprism/pkg/fs"
	"github.com/photoprism/photoprism/pkg/sanitize"
	"github.com/photoprism/photoprism/pkg/txt"
)

// SavePhotoAsYaml saves photo data as YAML file.
func SavePhotoAsYaml(p entity.Photo) {
	c := service.Config()

	// Write YAML sidecar file (optional).
	if !c.BackupYaml() {
		return
	}

	fileName := p.YamlFileName(c.OriginalsPath(), c.SidecarPath())

	if err := p.SaveAsYaml(fileName); err != nil {
		log.Errorf("photo: %s (update yaml)", err)
	} else {
		log.Debugf("photo: updated yaml file %s", txt.LogParam(filepath.Base(fileName)))
	}
}

// GetPhoto returns photo details as JSON.
//
// Route : GET /api/v1/photos/:uid
// Params:
// - uid (string) PhotoUID as returned by the API
func GetPhoto(router *gin.RouterGroup) {
	router.GET("/photos/:uid", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionRead)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		p, err := query.PhotoPreloadByUID(sanitize.IdString(c.Param("uid")))

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		c.IndentedJSON(http.StatusOK, p)
	})
}

// UpdatePhoto updates photo details and returns them as JSON.
//
// PUT /api/v1/photos/:uid
func UpdatePhoto(router *gin.RouterGroup) {
	router.PUT("/photos/:uid", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionUpdate)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		uid := sanitize.IdString(c.Param("uid"))
		m, err := query.PhotoByUID(uid)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		// TODO: Proof-of-concept for form handling - might need refactoring
		// 1) Init form with model values
		f, err := form.NewPhoto(m)

		if err != nil {
			Abort(c, http.StatusInternalServerError, i18n.ErrSaveFailed)
			return
		}

		// 2) Update form with values from request
		if err := c.BindJSON(&f); err != nil {
			Abort(c, http.StatusBadRequest, i18n.ErrBadRequest)
			return
		}

		// 3) Save model with values from form
		if err := entity.SavePhotoForm(m, f); err != nil {
			Abort(c, http.StatusInternalServerError, i18n.ErrSaveFailed)
			return
		} else if f.PhotoPrivate {
			FlushCoverCache()
		}

		PublishPhotoEvent(EntityUpdated, uid, c)

		event.SuccessMsg(i18n.MsgChangesSaved)

		p, err := query.PhotoPreloadByUID(uid)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		SavePhotoAsYaml(p)

		UpdateClientConfig()

		c.JSON(http.StatusOK, p)
	})
}

// GetPhotoDownload returns the primary file matching that belongs to the photo.
//
// Route :GET /api/v1/photos/:uid/dl
// Params:
// - uid (string) PhotoUID as returned by the API
func GetPhotoDownload(router *gin.RouterGroup) {
	router.GET("/photos/:uid/dl", func(c *gin.Context) {
		if InvalidDownloadToken(c) {
			c.Data(http.StatusForbidden, "image/svg+xml", brokenIconSvg)
			return
		}

		f, err := query.FileByPhotoUID(sanitize.IdString(c.Param("uid")))

		if err != nil {
			c.Data(http.StatusNotFound, "image/svg+xml", photoIconSvg)
			return
		}

		fileName := photoprism.FileName(f.FileRoot, f.FileName)

		if !fs.FileExists(fileName) {
			log.Errorf("photo: file %s is missing", txt.LogParam(f.FileName))
			c.Data(http.StatusNotFound, "image/svg+xml", photoIconSvg)

			// Set missing flag so that the file doesn't show up in search results anymore.
			logError("photo", f.Update("FileMissing", true))

			return
		}

		c.FileAttachment(fileName, f.DownloadName(DownloadName(c), 0))
	})
}

// GET /api/v1/photos/:uid/yaml
//
// Parameters:
//   uid: string PhotoUID as returned by the API
func GetPhotoYaml(router *gin.RouterGroup) {
	router.GET("/photos/:uid/yaml", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionExport)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		p, err := query.PhotoPreloadByUID(sanitize.IdString(c.Param("uid")))

		if err != nil {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}

		data, err := p.Yaml()

		if err != nil {
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}

		if c.Query("download") != "" {
			AddDownloadHeader(c, sanitize.IdString(c.Param("uid"))+fs.YamlExt)
		}

		c.Data(http.StatusOK, "text/x-yaml; charset=utf-8", data)
	})
}

// POST /api/v1/photos/:uid/approve
//
// Parameters:
//   uid: string PhotoUID as returned by the API
func ApprovePhoto(router *gin.RouterGroup) {
	router.POST("/photos/:uid/approve", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionUpdate)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		id := sanitize.IdString(c.Param("uid"))
		m, err := query.PhotoByUID(id)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		if err := m.Approve(); err != nil {
			log.Errorf("photo: %s", err.Error())
			AbortSaveFailed(c)
			return
		}

		SavePhotoAsYaml(m)

		PublishPhotoEvent(EntityUpdated, id, c)

		c.JSON(http.StatusOK, gin.H{"photo": m})
	})
}

// POST /api/v1/photos/:uid/like
//
// Parameters:
//   uid: string PhotoUID as returned by the API
func LikePhoto(router *gin.RouterGroup) {
	router.POST("/photos/:uid/like", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionLike)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		id := sanitize.IdString(c.Param("uid"))
		m, err := query.PhotoByUID(id)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		if err := m.SetFavorite(true); err != nil {
			log.Errorf("photo: %s", err.Error())
			AbortSaveFailed(c)
			return
		}

		SavePhotoAsYaml(m)

		PublishPhotoEvent(EntityUpdated, id, c)

		c.JSON(http.StatusOK, gin.H{"photo": m})
	})
}

// DELETE /api/v1/photos/:uid/like
//
// Parameters:
//   uid: string PhotoUID as returned by the API
func DislikePhoto(router *gin.RouterGroup) {
	router.DELETE("/photos/:uid/like", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionLike)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		id := sanitize.IdString(c.Param("uid"))
		m, err := query.PhotoByUID(id)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		if err := m.SetFavorite(false); err != nil {
			log.Errorf("photo: %s", err.Error())
			AbortSaveFailed(c)
			return
		}

		SavePhotoAsYaml(m)

		PublishPhotoEvent(EntityUpdated, id, c)

		c.JSON(http.StatusOK, gin.H{"photo": m})
	})
}

// POST /api/v1/photos/:uid/files/:file_uid/primary
//
// Parameters:
//   uid: string PhotoUID as returned by the API
//   file_uid: string File UID as returned by the API
func PhotoPrimary(router *gin.RouterGroup) {
	router.POST("/photos/:uid/files/:file_uid/primary", func(c *gin.Context) {
		s := Auth(SessionID(c), acl.ResourcePhotos, acl.ActionUpdate)

		if s.Invalid() {
			AbortUnauthorized(c)
			return
		}

		uid := sanitize.IdString(c.Param("uid"))
		fileUID := sanitize.IdString(c.Param("file_uid"))
		err := query.SetPhotoPrimary(uid, fileUID)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		PublishPhotoEvent(EntityUpdated, uid, c)

		event.SuccessMsg(i18n.MsgChangesSaved)

		p, err := query.PhotoPreloadByUID(uid)

		if err != nil {
			AbortEntityNotFound(c)
			return
		}

		c.JSON(http.StatusOK, p)
	})
}
