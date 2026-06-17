package virtual_network

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/previder/previder-go-sdk/client"
	"github.com/previder/terraform-provider-previder/internal/util/sorters"
)

const iaasBasePath = "v2/iaas/"

type resourceDataAddressPool struct {
	Id          types.String `tfsdk:"id"`
	Start       types.String `tfsdk:"start"`
	End         types.String `tfsdk:"end"`
	Mask        types.String `tfsdk:"mask"`
	Gateway     types.String `tfsdk:"gateway"`
	Type        types.String `tfsdk:"type"`
	NameServers types.List   `tfsdk:"nameservers"`
}

type addressPool struct {
	Id             string   `json:"id"`
	VirtualNetwork string   `json:"virtualNetwork"`
	Start          string   `json:"start"`
	End            string   `json:"end"`
	Mask           string   `json:"mask"`
	Gateway        string   `json:"gateway"`
	Type           string   `json:"type"`
	NameServers    []string `json:"nameServers"`
}

type addressPoolUpdate struct {
	Start       string   `json:"start"`
	End         string   `json:"end"`
	Mask        string   `json:"mask"`
	Gateway     string   `json:"gateway,omitempty"`
	Type        string   `json:"type"`
	NameServers []string `json:"nameServers,omitempty"`
}

func addressPoolSchemaAttributes() map[string]schema.Attribute {
	return map[string]schema.Attribute{
		"id": schema.StringAttribute{
			Computed: true,
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
			},
		},
		"start": schema.StringAttribute{
			Required: true,
		},
		"end": schema.StringAttribute{
			Required: true,
		},
		"mask": schema.StringAttribute{
			Required: true,
		},
		"gateway": schema.StringAttribute{
			Optional: true,
			Computed: true,
		},
		"type": schema.StringAttribute{
			Required: true,
		},
		"nameservers": schema.ListAttribute{
			ElementType: types.StringType,
			Optional:    true,
			Computed:    true,
		},
	}
}

func validateAddressPools(ctx context.Context, pools map[string]resourceDataAddressPool) error {
	for key, pool := range pools {
		if net.ParseIP(pool.Start.ValueString()) == nil {
			return fmt.Errorf("address pool %s has an invalid start address", key)
		}
		if net.ParseIP(pool.End.ValueString()) == nil {
			return fmt.Errorf("address pool %s has an invalid end address", key)
		}
		if net.ParseIP(pool.Mask.ValueString()) == nil {
			return fmt.Errorf("address pool %s has an invalid mask", key)
		}
		if !pool.Gateway.IsNull() && pool.Gateway.ValueString() != "" && net.ParseIP(pool.Gateway.ValueString()) == nil {
			return fmt.Errorf("address pool %s has an invalid gateway", key)
		}

		var nameservers []types.String
		diags := pool.NameServers.ElementsAs(ctx, &nameservers, false)
		if diags.HasError() {
			return fmt.Errorf("address pool %s has invalid nameserver values", key)
		}
		for _, nameserver := range nameservers {
			if net.ParseIP(nameserver.ValueString()) == nil {
				return fmt.Errorf("address pool %s has an invalid nameserver", key)
			}
		}
	}

	return nil
}

func listAddressPools(c *client.PreviderClient, networkId string) ([]addressPool, error) {
	var pools []addressPool
	if err := c.Get(iaasBasePath+"virtualnetwork/"+networkId+"/addresspool", &pools, nil); err != nil {
		return nil, err
	}

	sort.Slice(pools, func(i, j int) bool {
		return addressPoolSortKey(pools[i]) < addressPoolSortKey(pools[j])
	})

	return pools, nil
}

func syncAddressPools(ctx context.Context, c *client.PreviderClient, networkId string, planPools map[string]resourceDataAddressPool, statePools map[string]resourceDataAddressPool) error {
	if err := validateAddressPools(ctx, planPools); err != nil {
		return err
	}

	for _, key := range sorters.SortMapKeys(planPools) {
		pool := planPools[key]
		update := expandAddressPool(ctx, pool)
		poolId := ""
		if existingPool, ok := statePools[key]; ok && !existingPool.Id.IsNull() {
			poolId = existingPool.Id.ValueString()
		}

		if poolId == "" {
			var created addressPool
			if err := c.Post(iaasBasePath+"virtualnetwork/"+networkId+"/addresspool", update, &created); err != nil {
				return err
			}
			continue
		}

		var updated addressPool
		if err := c.Put(iaasBasePath+"virtualnetwork/"+networkId+"/addresspool/"+poolId, update, &updated); err != nil {
			return err
		}
	}

	for _, key := range sorters.SortMapKeys(statePools) {
		if _, ok := planPools[key]; ok {
			continue
		}

		statePool := statePools[key]
		if statePool.Id.IsNull() || statePool.Id.ValueString() == "" {
			continue
		}

		if err := c.Delete(iaasBasePath+"virtualnetwork/"+networkId+"/addresspool/"+statePool.Id.ValueString(), nil); err != nil {
			return err
		}
	}

	return nil
}

func expandAddressPool(ctx context.Context, pool resourceDataAddressPool) addressPoolUpdate {
	var nameservers []types.String
	_ = pool.NameServers.ElementsAs(ctx, &nameservers, false)

	update := addressPoolUpdate{
		Start: pool.Start.ValueString(),
		End:   pool.End.ValueString(),
		Mask:  pool.Mask.ValueString(),
		Type:  pool.Type.ValueString(),
	}

	if !pool.Gateway.IsNull() && pool.Gateway.ValueString() != "" {
		update.Gateway = pool.Gateway.ValueString()
	}

	if len(nameservers) > 0 {
		update.NameServers = make([]string, 0, len(nameservers))
		for _, nameserver := range nameservers {
			update.NameServers = append(update.NameServers, nameserver.ValueString())
		}
	}

	return update
}

func flattenAddressPools(ctx context.Context, pools []addressPool, planPools map[string]resourceDataAddressPool) map[string]resourceDataAddressPool {
	flattened := make(map[string]resourceDataAddressPool, len(pools))
	usedKeys := map[string]bool{}

	for index, pool := range pools {
		key := matchAddressPoolKey(pool, planPools, usedKeys)
		if key == "" {
			key = defaultAddressPoolKey(pool, index+1)
		}

		nameservers, _ := types.ListValueFrom(ctx, types.StringType, pool.NameServers)
		flattened[key] = resourceDataAddressPool{
			Id:          types.StringValue(pool.Id),
			Start:       types.StringValue(pool.Start),
			End:         types.StringValue(pool.End),
			Mask:        types.StringValue(pool.Mask),
			Gateway:     types.StringValue(pool.Gateway),
			Type:        types.StringValue(pool.Type),
			NameServers: nameservers,
		}
		usedKeys[key] = true
	}

	return flattened
}

func matchAddressPoolKey(pool addressPool, planPools map[string]resourceDataAddressPool, usedKeys map[string]bool) string {
	for _, key := range sorters.SortMapKeys(planPools) {
		if usedKeys[key] {
			continue
		}

		planned := planPools[key]
		if !planned.Id.IsNull() && planned.Id.ValueString() == pool.Id {
			return key
		}
	}

	for _, key := range sorters.SortMapKeys(planPools) {
		if usedKeys[key] {
			continue
		}

		if addressPoolMatches(planPools[key], pool) {
			return key
		}
	}

	return ""
}

func addressPoolMatches(planned resourceDataAddressPool, pool addressPool) bool {
	if planned.Start.ValueString() != pool.Start ||
		planned.End.ValueString() != pool.End ||
		planned.Mask.ValueString() != pool.Mask ||
		planned.Type.ValueString() != pool.Type ||
		planned.Gateway.ValueString() != pool.Gateway {
		return false
	}

	var plannedNameservers []types.String
	_ = planned.NameServers.ElementsAs(context.Background(), &plannedNameservers, false)
	if len(plannedNameservers) != len(pool.NameServers) {
		return false
	}

	for index, nameserver := range plannedNameservers {
		if nameserver.ValueString() != pool.NameServers[index] {
			return false
		}
	}

	return true
}

func defaultAddressPoolKey(pool addressPool, index int) string {
	base := strings.ToLower(pool.Type + "-" + pool.Start + "-" + pool.End)
	base = strings.ReplaceAll(base, ":", "-")
	base = strings.ReplaceAll(base, ".", "-")
	return fmt.Sprintf("%s-%d", base, index)
}

func addressPoolSortKey(pool addressPool) string {
	return strings.Join([]string{pool.Type, pool.Start, pool.End, pool.Mask, pool.Gateway, pool.Id}, "|")
}
