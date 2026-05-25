import { Container, getContainer } from "@cloudflare/containers";

export class ProxyContainer extends Container {
  defaultPort = 8317; // Matches the exposed port in Dockerfile
  sleepAfter = "10m"; // Stop the instance if requests not sent for 10 minutes

  constructor(state, env) {
    super(state, env);
    this.envVars = {
      PATH: "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
      TZ: "Asia/Shanghai",
      HOME: "/root",
    };
    if (env.OBJECTSTORE_ENDPOINT) this.envVars.OBJECTSTORE_ENDPOINT = env.OBJECTSTORE_ENDPOINT;
    if (env.OBJECTSTORE_BUCKET) this.envVars.OBJECTSTORE_BUCKET = env.OBJECTSTORE_BUCKET;
    if (env.OBJECTSTORE_ACCESS_KEY) this.envVars.OBJECTSTORE_ACCESS_KEY = env.OBJECTSTORE_ACCESS_KEY;
    if (env.OBJECTSTORE_SECRET_KEY) this.envVars.OBJECTSTORE_SECRET_KEY = env.OBJECTSTORE_SECRET_KEY;
  }

  async onStop(params) {
    console.error("Container stopped:", JSON.stringify(params));
  }

  onError(err) {
    console.error("Container error:", err.stack || err.message || err);
    throw err;
  }
}

export default {
  async fetch(request, env) {
    // We can use a single session ID for the proxy so it scales to max_instances automatically
    // or we can route per-user if we wanted. Using a default "global" session here.
    const sessionId = "global-proxy-session";

    // Get the container instance
    const containerInstance = getContainer(env.PROXY_CONTAINER, sessionId);

    // Pass the request to the container instance on its default port
    return containerInstance.fetch(request);
  },
};
