# Map

The Map page shows all nodes on an interactive map, color-coded by role.

[Screenshot: map with colored markers and controls panel]

## Map style and overlays

The layers button on the map (top-left) chooses what the map underneath the nodes looks like. The same picker is on the [Live](live.md) page (top-right) and the Repeater Planner.

- **Auto** (the default) follows the site theme: a light map in light mode, a dark map in dark mode.
- Or pick a specific map. They are grouped by kind:
  - **Street**: road maps, including gray and dark styles that keep the node markers easy to read.
  - **Topographic**: terrain and contour maps.
  - **Satellite**: aerial imagery. Satellite maps look the same in light and dark mode.
- **Overlays** are checkboxes drawn on top of whichever map you chose:
  - **Hillshade**: shaded relief, so hills and valleys stand out. Useful with any map when you are judging radio line of sight.
  - **Place labels**: town and road names, mainly for satellite imagery, which has none.

Your choice is saved in your browser: the picker opens on it after a reload and on the Live and Repeater Planner pages too. The small maps on node pages, Analytics and Home show it whenever it suits the current theme (a satellite map always does; a light map is not used in dark mode), and otherwise the theme's map. It is not shared with other visitors and not stored on the server. Pick **Auto** to go back to following the theme.

Which maps the picker lists is up to the site's operator: the newer providers are offered only once the operator turns them on.

Some maps in the Street group are vector maps, drawn by your browser rather than downloaded as pictures. They stay sharp at every zoom level. If your browser can't draw them (no WebGL), or they fail to load within 15 seconds, the page shows the regular map for the current theme instead. The small maps on node pages, Analytics and Home always use a regular map: there, a vector choice shows the theme's default map.

Which maps appear in the list is up to the site operator, who can turn groups of maps on and off and add their own tile server. See [Map Tile Providers](../deployment.md#map-tile-providers).

### Tropospheric ducting

When the site operator has turned it on, the layers button has an extra **Tropospheric ducting** checkbox under Overlays (on the Map, Live and Repeater Planner pages). If you do not see it, this site does not offer it.

Ducting is when a layer of air traps radio signals and carries them far beyond the normal range, so a distant node may suddenly be heard. The overlay shows a forecast as colour bands over the area around the mesh, like the usual tropospheric ducting forecast maps. The colour shows the mean refractivity gradient over the lowest 1.5 km of the air: how much the air's refractivity drops per kilometre of height, averaged from the ground up to 1.5 km (in N units per km). The faster it drops, the more signals bend back toward the ground and the further they travel. Normal air drops by about 40 N/km. Each colour is one range of the gradient, not a point on a continuous scale: a whole band has the same colour and the colour changes at the band's edge, so you can read the strength off the legend.

- **No colour**: normal propagation (above -44 N/km).
- **Purple, blue and cyan**: enhanced propagation (-44 to -48, -48 to -54 and -54 to -58 N/km). Signals bend more than usual and reach a little further.
- **Green, yellow and orange**: strong (-58 to -62, -62 to -70 and -70 to -80 N/km). Signals often reach well beyond the usual range; the further along the scale, the stronger the effect.
- **Red and pink**: very strong (-80 to -90, and -90 N/km and beyond).
- A **dashed outline** marks a grid cell where a duct can carry LoRa 868/915 MHz (it traps signals at 1 GHz or lower). Mesh links can stretch over long distances there. A duct without the outline is too thin for LoRa and matters only for higher frequencies. The outline comes from the duct found in the model's layers, not from the colour, so it can sit on any band, or on no colour at all.
- A faint **dotted line** marks the edge of the forecast area. There is no forecast beyond it, and the colours fade out towards it, so a blank map outside the line means "not covered", not "no ducting".
- Areas without forecast data are not drawn; the field fades out at their edge.

The legend shows the same bands as a bar, labelled Normal, Enhanced, Strong and Very strong. Use the **Forecast time** slider to move through the coming hours. Click anywhere in the field for the details of that grid cell: the gradient over the lowest 1.5 km, the type of duct found in the model's layers (normal, super-refraction, elevated duct or surface-based duct), base and top of the duct (metres above ground), strength, the lowest frequency it can trap and whether it can carry 868 and 915 MHz. An elevated duct is a layer above the ground and mostly helps nodes that sit inside it, such as repeaters on high sites; a surface-based duct reaches the ground. The legend also shows the weather model, the data attribution and when the forecast was last updated.

It is an indicator only. The weather model sees layers about 200 m or thicker, so thinner ducts, such as most over the sea, do not appear, and each grid cell covers tens of kilometres: the band edges are interpolated between cell centres and show no detail finer than that. A clear map does not mean no long-distance reception, and a coloured area does not promise it. See also [Why did a node far away hear my packet?](faq.md#12-why-did-a-node-far-away-hear-my-packet)

## Marker shapes and colors

Each node role has a distinct shape and color:

| Role | Shape | Default Color |
|------|-------|---------------|
| Repeater | Diamond | Red |
| Companion | Circle | Blue |
| Room | Square | Green |
| Sensor | Triangle | Orange |
| Observer | Star | Purple |

Stale nodes (not heard recently) appear faded.

## Hash labels

Repeaters can display their short mesh hash ID instead of a plain marker. Toggle **Hash Labels** in the map controls to switch between icon markers and hash-labeled markers.

## Map controls

Open the controls panel with the ⚙️ button (top-right corner).

### Node types

Check or uncheck roles to show/hide them on the map. All roles are visible by default.

### Byte size filter

Filter nodes by packet size category: All, Small, Medium, Large.

### Status filter

Show only active, degraded, or silent nodes.

### Last heard filter

Limit the map to nodes heard within a time window (e.g., 24h, 7d, 30d).

### Clustering

Enable clustering to group nearby nodes into cluster bubbles. Zoom in to expand clusters.

### Neighbor filter

Select a reference node to highlight only its direct neighbors.

## Show Route

Click a node marker, then click **Show Route** in the popup to see the paths packets take to reach that node. Routes are drawn as lines between nodes.

## Popups

Click any marker to see:

- Node name and role
- Public key
- Last seen timestamp
- Link to the full node detail page

## Tips

- Zoom in on dense areas to see individual nodes
- Use the role checkboxes to isolate repeaters and understand coverage
- The neighbor filter is great for seeing which nodes can directly hear each other
- Node colors are [customizable](customization.md) in the theme settings
- Turn on the **Hillshade** overlay to see which nodes have high ground between them
- A gray street map makes colored markers stand out on a busy mesh
