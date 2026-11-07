// A local Wails client of the existing headless Server. The Go bridge supplies
// authentication; JavaScript never receives the local owner credential.
export function managedFetch(native, runtime, origin, makeID = () => Array.from(crypto.getRandomValues(new Uint8Array(16)), n => n.toString(16).padStart(2, '0')).join('')) {
  return async (input, options = {}) => {
    const request = new Request(input, options)
    const url = new URL(request.url)
    if (url.origin !== origin || !url.pathname.startsWith('/v1/')) throw new Error('Local transport only accepts this Server’s API')
    if (request.signal.aborted) throw new DOMException('Request cancelled', 'AbortError')
    const id = makeID(), reader = request.body?.getReader()
    let controller, responseReady = false, responseStatus = 0, finished = false, pendingAck = false, unsubscribe
    let resolveResponse, rejectResponse
    const response = new Promise((resolve, reject) => { resolveResponse=resolve;rejectResponse=reject })
    const acknowledge = () => {
      if (pendingAck && !finished && controller.desiredSize > 0) { pendingAck=false;Promise.resolve(native.AckManagedRequest(id)).catch(fail) }
    }
    function cleanup() { unsubscribe?.();request.signal.removeEventListener('abort', abort) }
    function fail(error) {
      if (finished) return
      finished=true;cleanup();reader?.cancel().catch(()=>{})
      Promise.resolve(native.CancelManagedRequest(id)).catch(()=>{})
      if (responseReady) controller.error(error);else {controller.close();rejectResponse(error)}
    }
    const abort = () => fail(new DOMException('Request cancelled', 'AbortError'))
    const body = new ReadableStream({start(value){controller=value},pull:acknowledge,cancel(){finished=true;cleanup();reader?.cancel().catch(()=>{});return native.CancelManagedRequest(id)}})
    unsubscribe = runtime.EventsOn(`praimate:managed:${id}`, event => {
      if (finished) return
      if (event.error) {fail(new Error(event.error));return}
      if (event.status) {
        try {responseReady=true;responseStatus=event.status;resolveResponse(new Response([204,205,304].includes(event.status)?null:body,{status:event.status,headers:event.headers}))}
        catch(error){responseReady=false;fail(error)}
      }
      if (event.data) {
        pendingAck=true;controller.enqueue(Uint8Array.from(atob(event.data), value=>value.charCodeAt(0)));acknowledge()
      }
      if (event.done) {finished=true;cleanup();controller.close()}
    })
    request.signal.addEventListener('abort', abort, {once:true})
    async function upload() {
      if (!reader) return
      try {
        while (!finished) {
          const {value,done}=await reader.read()
          if (finished) return
          if (done) {await native.WriteManagedRequest(id,'',true);return}
          for (let offset=0;offset<value.length;offset+=48*1024) {
            let bytes='';for(const byte of value.subarray(offset,offset+48*1024))bytes+=String.fromCharCode(byte)
            await native.WriteManagedRequest(id,btoa(bytes),false)
            if(finished)return
          }
        }
      } catch(error) {if(!finished&&responseStatus<400)fail(error)}
    }
    Promise.resolve().then(()=>native.BeginManagedRequest(id,url.pathname+url.search,request.method,request.headers.get('Content-Type')||'',request.headers.get('X-Praimate-Client')||'',!!reader))
      .then(()=>{if(finished)return native.CancelManagedRequest(id);return upload()}).catch(fail)
    return response
  }
}
