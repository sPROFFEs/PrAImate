package com.praimate.mobile;

import android.content.Context;
import android.content.Intent;
import android.webkit.WebView;
import androidx.test.core.app.ActivityScenario;
import androidx.test.ext.junit.runners.AndroidJUnit4;
import androidx.test.platform.app.InstrumentationRegistry;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import org.json.JSONObject;
import org.junit.Test;
import org.junit.runner.RunWith;
import java.io.BufferedReader;
import java.io.InputStreamReader;
import java.net.InetAddress;
import java.net.ServerSocket;
import java.net.Socket;
import java.nio.charset.StandardCharsets;
import java.util.Collections;
import java.util.concurrent.CountDownLatch;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;
import static org.junit.Assert.*;

/** Actual Android WebView -> native HTTP -> Keystore -> authenticated HTTP. */
@RunWith(AndroidJUnit4.class)
public class NativeBridgeTest {
    @Test public void pairingKeepsTheCredentialNativeAndStreamsAuthenticatedResponses()throws Exception{
        Context context=InstrumentationRegistry.getInstrumentation().getTargetContext();
        String token="synthetic-native-test-token-32-characters-long";
        CountDownLatch complete=new CountDownLatch(1);
        AtomicReference<String> browserResult=new AtomicReference<>(),authorization=new AtomicReference<>();
        AtomicReference<Throwable> serverError=new AtomicReference<>();AtomicInteger hostCalls=new AtomicInteger();
        try(ServerSocket server=new ServerSocket(0,2,InetAddress.getByName("127.0.0.1"))){
            server.setSoTimeout(30000);
            Thread fixture=new Thread(()->{
                try{for(int request=0;request<2;request++)try(Socket socket=server.accept()){
                    socket.setSoTimeout(15000);
                    BufferedReader reader=new BufferedReader(new InputStreamReader(socket.getInputStream(),StandardCharsets.UTF_8));
                    String first=reader.readLine(),line;int length=0;
                    while((line=reader.readLine())!=null&&!line.isEmpty()){
                        if(line.toLowerCase(java.util.Locale.ROOT).startsWith("content-length:"))length=Integer.parseInt(line.substring(15).trim());
                        if(request==1&&line.toLowerCase(java.util.Locale.ROOT).startsWith("authorization:"))authorization.set(line.substring(14).trim());
                    }
                    for(int i=0;i<length;i++)reader.read();
                    if(first==null)throw new AssertionError("No HTTP request");
                    String body=request==0?"{\"token\":\""+token+"\"}":"{\"ready\":true}";
                    byte[] bytes=body.getBytes(StandardCharsets.UTF_8);
                    socket.getOutputStream().write(("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: "+bytes.length+"\r\nConnection: close\r\n\r\n").getBytes(StandardCharsets.UTF_8));socket.getOutputStream().write(bytes);socket.getOutputStream().flush();
                }}catch(Throwable error){serverError.set(error);complete.countDown();}
            },"native-http-fixture");fixture.setDaemon(true);fixture.start();
            Vault vault=new Vault(context);String url="http://127.0.0.1:"+server.getLocalPort(),id=vault.add("Native fixture",url);
            AtomicReference<WebView> web=new AtomicReference<>();AtomicReference<NativeBridge> bridge=new AtomicReference<>();
            try(ActivityScenario<MainActivity> activity=ActivityScenario.launch(new Intent(context,MainActivity.class))){
                activity.onActivity(host->{
                    assertTrue("Update Android System WebView",WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER));
                    WebView view=new WebView(host);web.set(view);view.getSettings().setJavaScriptEnabled(true);host.setContentView(view);
                    NativeBridge nativeBridge=new NativeBridge(vault,id,url,(action,args,reply)->hostCalls.incrementAndGet());bridge.set(nativeBridge);
                    WebViewCompat.addWebMessageListener(view,"PraimateNative",Collections.singleton(Endpoint.APP_ORIGIN),nativeBridge);
                    WebViewCompat.addWebMessageListener(view,"TestResult",Collections.singleton(Endpoint.APP_ORIGIN),(v,message,origin,main,reply)->{if(main){browserResult.set(message.getData());complete.countDown();}});
                    String script="let bodies={},pair='';PraimateNative.onmessage=e=>{const m=JSON.parse(e.data);if(m.type==='error'){TestResult.postMessage(JSON.stringify(m));return}if(m.type==='data'){bodies[m.id]=(bodies[m.id]||'')+atob(m.data);PraimateNative.postMessage(JSON.stringify({id:m.id,action:'ack'}))}if(m.type==='done'){if(m.id==='pair'){pair=JSON.parse(bodies.pair).token;PraimateNative.postMessage(JSON.stringify({id:'status',action:'http',args:{path:'/v1/status',headers:{Authorization:'Bearer browser-must-not-override'}}}))}else{TestResult.postMessage(JSON.stringify({pair,ready:JSON.parse(bodies.status).ready,bodies}))}}};PraimateNative.postMessage(JSON.stringify({id:'pair',action:'http',args:{path:'/v1/pair',method:'POST',headers:{'Content-Type':'application/json'},body:'{\"code\":\"12345678\"}'}}));";
                    view.loadDataWithBaseURL(Endpoint.APP_ORIGIN+"/test","<!doctype html><script>"+script+"</script>","text/html","UTF-8",null);
                });
                try{
                    assertTrue("Native WebView/HTTP did not finish",complete.await(45,TimeUnit.SECONDS));
                    if(serverError.get()!=null)throw new AssertionError(serverError.get());
                    assertNotNull(browserResult.get());JSONObject result=new JSONObject(browserResult.get());
                    assertEquals("android-keystore-session",result.getString("pair"));assertTrue(result.getBoolean("ready"));
                    assertFalse(browserResult.get().contains(token));assertEquals("Bearer "+token,authorization.get());
                    assertEquals(token,new Vault(context).profile(id).getString("token"));assertEquals(0,hostCalls.get());
                }finally{
                    if(bridge.get()!=null)bridge.get().close();
                    InstrumentationRegistry.getInstrumentation().runOnMainSync(()->{
                        WebView view=web.get();
                        if(view!=null){if(view.getParent() instanceof android.view.ViewGroup)((android.view.ViewGroup)view.getParent()).removeView(view);view.destroy();}
                    });
                }
            }finally{vault.remove(id);}
        }
    }
}
