package openstack

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
	api "github.com/konveyor/forklift-controller/pkg/apis/forklift/v1beta1"
	model "github.com/konveyor/forklift-controller/pkg/controller/provider/model/openstack"
	"github.com/konveyor/forklift-controller/pkg/controller/provider/web/base"
	libmodel "github.com/konveyor/forklift-controller/pkg/lib/inventory/model"
)

// Routes.
const (
	WorkloadCollection = "workloads"
	WorkloadsRoot      = ProviderRoot + "/" + WorkloadCollection
	WorkloadRoot       = WorkloadsRoot + "/:" + VMParam
)

// Virtual Machine handler.
type WorkloadHandler struct {
	Handler
}

// Add routes to the `gin` router.
func (h *WorkloadHandler) AddRoutes(e *gin.Engine) {
	e.GET(WorkloadRoot, h.Get)
}

// List resources in a REST collection.
func (h WorkloadHandler) List(ctx *gin.Context) {
}

// Get a specific REST resource.
func (h WorkloadHandler) Get(ctx *gin.Context) {
	status, err := h.Prepare(ctx)
	if status != http.StatusOK {
		ctx.Status(status)
		base.SetForkliftError(ctx, err)
		return
	}
	m := &model.VM{
		Base: model.Base{
			ID: ctx.Param(VMParam),
		},
	}
	db := h.Collector.DB()
	err = db.Get(m)
	if errors.Is(err, model.NotFound) {
		ctx.Status(http.StatusNotFound)
		return
	}
	defer func() {
		if err != nil {
			log.Trace(
				err,
				"url",
				ctx.Request.URL)
			ctx.Status(http.StatusInternalServerError)
		}
	}()
	if err != nil {
		return
	}
	h.Detail = model.MaxDetail
	r := Workload{}
	r.VM.With(m)
	err = r.Expand(h.Collector.DB())
	if err != nil {
		return
	}
	r.Link(h.Provider)

	ctx.JSON(http.StatusOK, r)
}

// Workload
type Workload struct {
	SelfLink string `json:"selfLink"`
	XVM
}

// Build self link (URI).
func (r *Workload) Link(p *api.Provider) {
	r.SelfLink = base.Link(
		WorkloadRoot,
		base.Params{
			base.ProviderParam: string(p.UID),
			VMParam:            r.ID,
		})
	r.XVM.Link(p)
}

// Expanded: VM.
type XVM struct {
	VM
	Image           Image    `json:"image"`
	AttachedVolumes []Volume `json:"attached_volumes"`
}

// Expand references.
func (r *XVM) Expand(db libmodel.DB) (err error) {
	image := model.Image{Base: model.Base{
		ID: r.ImageID,
	}}
	db.Get(&image)
	r.Image.With(&image)

	var volumes []Volume
	for _, av := range r.VM.AttachedVolumes {
		volumeModel := model.Volume{
			Base: model.Base{ID: av.ID},
		}
		db.Get(&volumeModel)
		volume := &Volume{}
		volume.With(&volumeModel)
		volumes = append(volumes, *volume)
	}
	r.AttachedVolumes = volumes

	return
}

// Build self link (URI).
func (r *XVM) Link(p *api.Provider) {
	r.VM.Link(p)
}

// Expand the workload.
func (r *Workload) Expand(db libmodel.DB) (err error) {
	// VM
	err = r.XVM.Expand(db)
	if err != nil {
		return err
	}

	return
}
