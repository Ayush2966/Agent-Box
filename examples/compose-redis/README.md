# Compose example

Needs Docker with the compose plugin. Each box gets its own Compose project
(`agentbox-<repo>-<box>`) with its own Redis on its own host port.

    agentbox new a && agentbox new b
    curl http://a.localhost:7777/   # {"box":"a","redisPort":20001,"hits":1}
    curl http://b.localhost:7777/   # {"box":"b","redisPort":20003,"hits":1}
    docker compose ls               # two agentbox-… projects
    agentbox rm a && agentbox rm b  # containers and volumes gone
