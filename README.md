# lol

![lolbunny](/lol.png)

(image from https://www.deviantart.com/u-lol360/art/U-LOL-ICON-Bonnie-the-Bunny-533168267 )

One of the many clones of `bunnylol` from Meta.

## Configuration

See [config.yaml.example](/config.yaml.example).

If you are using an old JSON configuration file, you can either keep using it (since JSON is a subset of YAML), or convert it to canonical YAML with `yq -y < your-config.json > your-config.yaml`.
Note: if you're using the Go-based `yq` instead of the Python-based `yq`, use `-P` instead of `-y`.
