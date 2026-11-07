package com.praimate.mobile;

import android.os.Handler;
import android.os.Looper;
import android.util.Base64;
import android.webkit.WebView;
import androidx.webkit.JavaScriptReplyProxy;
import androidx.webkit.WebMessageCompat;
import androidx.webkit.WebViewCompat;
import org.json.JSONObject;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.nio.charset.StandardCharsets;
import java.util.Iterator;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Semaphore;
import java.util.concurrent.TimeUnit;

/** A main-frame-only message protocol; remote pages never receive this bridge. */
public final class NativeBridge implements WebViewCompat.WebMessageListener {
    public interface Host {
        void action(String action, JSONObject args, Reply reply) throws Exception;
        default void credentialChanged(String profile) {}
        default void accessRequired(String profile) {}
    }
    public interface Reply { void result(Object value); void error(String message); }
    private final Vault vault;
    private final String profile;
    private final String serverURL;
    private final Host host;
    private final Handler main = new Handler(Looper.getMainLooper());
    private final ExecutorService executor = Executors.newFixedThreadPool(12);
    private final Map<String,Transfer> transfers = new ConcurrentHashMap<>();
    private volatile boolean closed;
    private static final Set<String> METHODS = new java.util.HashSet<>(java.util.Arrays.asList("GET","POST","PUT","HEAD"));
    private static final Set<String> HEADERS = new java.util.HashSet<>(java.util.Arrays.asList("content-type","range","if-range","last-event-id","x-praimate-client"));

    private static final class Transfer {
        volatile HttpURLConnection connection;
        volatile boolean cancelled;
        final Semaphore ready = new Semaphore(0);
        void cancel(){cancelled=true;ready.release();if(connection!=null)connection.disconnect();}
    }
    public NativeBridge(Vault vault,String profile,String serverURL,Host host){this.vault=vault;this.profile=profile;this.serverURL=serverURL;this.host=host;}

    private void send(JavaScriptReplyProxy proxy,JSONObject payload){if(!closed)main.post(()->{if(!closed&&androidx.webkit.WebViewFeature.isFeatureSupported(androidx.webkit.WebViewFeature.WEB_MESSAGE_LISTENER))try{proxy.postMessage(payload.toString());}catch(RuntimeException ignored){}});}
    private JSONObject message(String id,String type){try{return new JSONObject().put("id",id).put("type",type);}catch(Exception error){throw new IllegalStateException(error);}}

    @Override public void onPostMessage(WebView view, WebMessageCompat message, android.net.Uri origin, boolean mainFrame, JavaScriptReplyProxy proxy) {
        if(closed||!mainFrame||!Endpoint.appOrigin(origin.toString()))return;
        String id="";
        try {
            String raw=message.getData();if(raw==null||raw.length()>24*1024*1024)throw new IllegalArgumentException("Request exceeds native transport limit");
            JSONObject request=new JSONObject(raw);id=request.getString("id");
            if(!id.matches("[A-Za-z0-9_-]{1,80}"))throw new IllegalArgumentException("Invalid native request ID");
            String action=request.getString("action");
            if(action.equals("ack")){Transfer transfer=transfers.get(id);if(transfer!=null&&transfer.ready.availablePermits()==0)transfer.ready.release();return;}
            if(action.equals("cancel")){Transfer transfer=transfers.remove(id);if(transfer!=null)transfer.cancel();return;}
            final String requestID=id;
            Reply reply=new Reply(){public void result(Object value){try{send(proxy,NativeBridge.this.message(requestID,"result").put("result",value==null?JSONObject.NULL:value));}catch(Exception ignored){}} public void error(String text){try{send(proxy,NativeBridge.this.message(requestID,"error").put("message",text));}catch(Exception ignored){}}};
            JSONObject args=request.optJSONObject("args");if(args==null)args=new JSONObject();
            if(action.equals("http")) {
                if(transfers.size()>=12)throw new IllegalStateException("Too many native requests; wait for the current transfer");
                Transfer transfer=new Transfer();if(transfers.putIfAbsent(id,transfer)!=null)throw new IllegalStateException("Native request already exists");
                JSONObject input=args;executor.execute(()->http(requestID,input,proxy,reply,transfer));
            } else host.action(action,args,reply);
        } catch(Exception error) {
            try{send(proxy,message(id,"error").put("message",publicMessage(error)));}catch(Exception ignored){}
        }
    }

    private void http(String id,JSONObject args,JavaScriptReplyProxy proxy,Reply reply,Transfer transfer) {
        try {
            String path=Endpoint.apiPath(args.getString("path")),method=args.optString("method","GET");
            if(!METHODS.contains(method))throw new IllegalArgumentException("Unsupported HTTP method");
            long credentialVersion=vault.profile(profile).optLong("credentialVersion");
            HttpURLConnection connection=new ServerConnection(vault).open(profile,path,method,true,serverURL);transfer.connection=connection;
            if(transfer.cancelled)return;
            JSONObject headers=args.optJSONObject("headers");
            if(headers!=null)for(Iterator<String> it=headers.keys();it.hasNext();){String name=it.next();String value=headers.optString(name);if(HEADERS.contains(name.toLowerCase(java.util.Locale.ROOT))&&!value.contains("\n")&&!value.contains("\r"))connection.setRequestProperty(name,value);}
            String body=args.optString("body","");
            if(!body.isEmpty()){
                byte[] bytes=body.getBytes(StandardCharsets.UTF_8);if(bytes.length>16*1024*1024)throw new IllegalArgumentException("Request exceeds the 16 MiB API limit");
                connection.setDoOutput(true);connection.setFixedLengthStreamingMode(bytes.length);
                try(OutputStream output=connection.getOutputStream()){output.write(bytes);}
            }
            int status=ServerConnection.responseCode(connection);
            JSONObject responseHeaders=new JSONObject();
            for(Map.Entry<String,java.util.List<String>> entry:connection.getHeaderFields().entrySet())if(entry.getKey()!=null&&!entry.getKey().equalsIgnoreCase("Set-Cookie"))responseHeaders.put(entry.getKey(),String.join(", ",entry.getValue()));
            boolean pairing=path.equals("/v1/pair")&&status>=200&&status<300;
            InputStream input=status>=400?connection.getErrorStream():connection.getInputStream();
            if(pairing){
                String text=readText(input,64*1024);JSONObject paired=new JSONObject(text);String token=paired.getString("token");
                if(transfer.cancelled||closed)return;
                vault.finishPairing(profile,connection.getURL().getProtocol()+"://"+connection.getURL().getAuthority(),credentialVersion,token);paired.put("token","android-keystore-session");
                main.post(()->{try{if(vault.profile(profile).optLong("credentialVersion")==credentialVersion+1)host.credentialChanged(profile);}catch(Exception ignored){}});
                send(proxy,message(id,"headers").put("status",status).put("headers",responseHeaders));
                chunk(id,paired.toString().getBytes(StandardCharsets.UTF_8),proxy,transfer);
            } else {
                send(proxy,message(id,"headers").put("status",status).put("headers",responseHeaders));
                if(input!=null)try(InputStream stream=input){
                    byte[] buffer=new byte[32768];int count;long total=0;boolean events=path.startsWith("/v1/events");
                    while(!transfer.cancelled&&(count=stream.read(buffer))!=-1){
                        total+=count;if(!events&&total>32L*1024*1024)throw new IllegalStateException("Response exceeds native preview limit; use file download");
                        chunk(id,java.util.Arrays.copyOf(buffer,count),proxy,transfer);
                    }
                }
            }
            if(!transfer.cancelled)send(proxy,message(id,"done"));
        } catch(Exception error){if(!transfer.cancelled){reply.error(publicMessage(error));if(error instanceof AccessSession.Required)main.post(()->{if(!closed)host.accessRequired(profile);});}}
        finally{transfers.remove(id,transfer);if(transfer.connection!=null)transfer.connection.disconnect();}
    }

    private void chunk(String id,byte[] bytes,JavaScriptReplyProxy proxy,Transfer transfer)throws Exception{
        send(proxy,message(id,"data").put("data",Base64.encodeToString(bytes,Base64.NO_WRAP)));
        if(!transfer.ready.tryAcquire(45,TimeUnit.SECONDS)&&!transfer.cancelled)throw new IllegalStateException("Client paused; reconnect to recover the server session");
    }

    public static String readText(InputStream input,int limit)throws Exception{
        if(input==null)return "";
        try(InputStream stream=input;java.io.ByteArrayOutputStream output=new java.io.ByteArrayOutputStream()){
            byte[] buffer=new byte[8192];int count;while((count=stream.read(buffer))!=-1){if(output.size()+count>limit)throw new IllegalArgumentException("Response exceeds limit");output.write(buffer,0,count);}return output.toString("UTF-8");
        }
    }
    public static String publicMessage(Exception error){
        if(error instanceof java.security.cert.CertificateException)return "Certificate is invalid or expired. Check the certificate file and device/server clocks.";
        if(error instanceof javax.net.ssl.SSLException)return "Server certificate is not trusted or does not match its hostname. Import the server CA from Servers.";
        if(error instanceof java.net.SocketTimeoutException)return "Server connection timed out; running server tasks are retained";
        if(error instanceof java.net.ConnectException||error instanceof java.net.UnknownHostException)return "Server is unreachable. Check the address, VPN and network connection.";
        if(error instanceof IllegalArgumentException||error instanceof IllegalStateException)return error.getMessage()==null?"Operation failed":error.getMessage();
        return "Connection failed. Reconnect and inspect the retained server operation before retrying.";
    }
    public void cancelRequests(){for(Transfer transfer:transfers.values())transfer.cancel();transfers.clear();}
    public void close(){closed=true;cancelRequests();executor.shutdownNow();}
}
