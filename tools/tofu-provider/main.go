package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/hashicorp/terraform-plugin-sdk/v2/plugin"
	"github.com/miciav/minikube-cloud-lab/internal/api"
	"github.com/miciav/minikube-cloud-lab/internal/cloud"
)

func main() { plugin.Serve(&plugin.ServeOpts{ProviderFunc: provider}) }

func provider() *schema.Provider {
	return &schema.Provider{
		Schema: map[string]*schema.Schema{"endpoint": {Type: schema.TypeString, Optional: true, Default: "http://127.0.0.1:8080", ValidateFunc: validation.IsURLWithHTTPorHTTPS}},
		ConfigureContextFunc: func(_ context.Context, d *schema.ResourceData) (any, diag.Diagnostics) {
			return api.NewClient(d.Get("endpoint").(string)), nil
		},
		ResourcesMap: map[string]*schema.Resource{"lab_instance": resource(false), "lab_cluster": resource(true)},
	}
}

func absent(err error) bool {
	var e *api.Error
	return errors.As(err, &e) && e.Status == 404
}

func wait(ctx context.Context, c *api.Client, op cloud.Operation) error {
	for {
		switch op.Status {
		case "DONE":
			return nil
		case "ERROR":
			return fmt.Errorf("operation %s: %s", op.ID, op.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
		var err error
		op, err = c.Operation(ctx, op.ID)
		if err != nil {
			return err
		}
	}
}

func resource(cluster bool) *schema.Resource {
	fields := map[string]*schema.Schema{
		"name":         {Type: schema.TypeString, Required: true, ForceNew: true},
		"machine_type": {Type: schema.TypeString, Optional: true, Default: "lab-small", ForceNew: true},
	}
	if cluster {
		for _, key := range []string{"min_nodes", "max_nodes"} {
			fields[key] = &schema.Schema{Type: schema.TypeInt, Optional: true, Default: 1, ForceNew: true, ValidateFunc: validation.IntAtLeast(1)}
		}
	} else {
		fields["user_data"] = &schema.Schema{Type: schema.TypeString, Optional: true, ForceNew: true, Sensitive: true}
		fields["image"] = &schema.Schema{Type: schema.TypeString, Optional: true, Default: "ubuntu-26.04", ForceNew: true}
		fields["ip"] = &schema.Schema{Type: schema.TypeString, Computed: true}
	}
	read := func(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
		c := meta.(*api.Client)
		var err error
		values := map[string]any{"name": d.Id()}
		if cluster {
			var r cloud.Cluster
			r, err = c.Cluster(ctx, d.Id())
			values["machine_type"], values["min_nodes"], values["max_nodes"] = r.MachineType, r.MinMachines, r.MaxMachines
		} else {
			var r cloud.Instance
			r, err = c.Instance(ctx, d.Id())
			values["machine_type"], values["image"], values["ip"] = r.MachineType, r.Image, r.IP
		}
		if absent(err) {
			d.SetId("")
			return nil
		}
		if err != nil {
			return diag.FromErr(err)
		}
		for k, v := range values {
			if err := d.Set(k, v); err != nil {
				return diag.FromErr(err)
			}
		}
		return nil
	}
	return &schema.Resource{Schema: fields, ReadContext: read,
		CreateContext: func(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
			c := meta.(*api.Client)
			name := d.Get("name").(string)
			kind := d.Get("machine_type").(string)
			var op cloud.Operation
			var err error
			if cluster {
				op, err = c.CreateCluster(ctx, name, kind, d.Get("min_nodes").(int), d.Get("max_nodes").(int))
			} else {
				op, err = c.CreateInstance(ctx, name, d.Get("image").(string), kind, d.Get("user_data").(string))
			}
			if err != nil {
				return diag.FromErr(err)
			}
			d.SetId(name)
			if err = wait(ctx, c, op); err != nil {
				return diag.FromErr(err)
			}
			return read(ctx, d, meta)
		},
		DeleteContext: func(ctx context.Context, d *schema.ResourceData, meta any) diag.Diagnostics {
			c := meta.(*api.Client)
			var op cloud.Operation
			var err error
			if cluster {
				op, err = c.DeleteCluster(ctx, d.Id())
			} else {
				op, err = c.DeleteInstance(ctx, d.Id())
			}
			if absent(err) {
				d.SetId("")
				return nil
			}
			if err == nil {
				err = wait(ctx, c, op)
			}
			if err != nil {
				return diag.FromErr(err)
			}
			d.SetId("")
			return nil
		},
	}
}
