package api

import "strings"

// The upstream control panel is distributed as a single-file asset. Restrict
// retries to known idempotent reads, leaving every write and auth denial alone.
// Match the complete adapter method rather than replacing arbitrary URL strings.
const upstreamManagementGET = "async get(e,t){return(await this.instance.get(e,t)).data}"

const resilientManagementGET = `async get(e,t){
 let attempts=0;
 for(;;){
  try{return(await this.instance.get(e,t)).data}
  catch(error){
   const retryablePath=['/auth-files','/oauth-excluded-models','/oauth-model-alias'].includes(e);
   const transient=error.code==='ERR_NETWORK'||[502,503,504].includes(error.status);
   if(!retryablePath||!transient||attempts>=2||t?.signal?.aborted||error.code==='ERR_CANCELED'||error.status===401||error.status===403)throw error;
   await new Promise(resolve=>setTimeout(resolve,250*Math.pow(2,attempts++)));
   if(t?.signal?.aborted)throw error;
  }
 }
}`

func injectManagementReadRetry(html string) string {
	if strings.Count(html, upstreamManagementGET) != 1 {
		return html
	}
	return strings.Replace(html, upstreamManagementGET, resilientManagementGET, 1)
}
