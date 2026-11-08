package com.praimate.mobile;

import android.Manifest;
import android.app.Activity;
import android.app.AlertDialog;
import android.content.ClipData;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.database.Cursor;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.provider.OpenableColumns;
import android.webkit.PermissionRequest;
import android.webkit.WebChromeClient;
import android.webkit.WebResourceRequest;
import android.webkit.WebResourceResponse;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.EditText;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;
import androidx.webkit.WebViewAssetLoader;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import org.json.JSONArray;
import org.json.JSONObject;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.security.cert.CertificateFactory;
import java.security.cert.X509Certificate;
import java.util.ArrayList;
import java.util.Collections;
import java.util.HashMap;
import java.util.List;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

public final class MainActivity extends androidx.activity.ComponentActivity implements NativeBridge.Host {
    private static final String HOME=Endpoint.APP_ORIGIN+"/app/web/index.html";
    private static final int PICK_UPLOAD=10,PICK_DOWNLOAD=11,PICK_CA=12,PICK_BROWSER=13,ACCESS_LOGIN=14,MICROPHONE=20,NOTIFICATIONS=21;
    private static final long FILE_LIMIT=512L*1024*1024;
    private final ExecutorService io=Executors.newFixedThreadPool(3);
    private Vault vault;
    private AppUpdates updates;
    private String profileID="",certificateProfile="",activeServerURL="";
    private int probeGeneration;
    private WebView web;
    private NativeBridge bridge;
    private NativeBridge.Reply pickerReply;
    private JSONObject download;
    private PermissionRequest audioPermission;
    private NativeBridge.Reply monitorReply;
    private android.webkit.ValueCallback<Uri[]> browserPicker;
    private volatile boolean destroyed;
    private String accessProfile="",accessURL="";
    private long accessVersion;
    private static boolean accessInProgress;
    private AlertDialog accessDialog;
    private final java.util.Set<String> accessPrompted=new java.util.HashSet<>();
    private static final java.util.concurrent.CopyOnWriteArrayList<java.lang.ref.WeakReference<MainActivity>> activities=new java.util.concurrent.CopyOnWriteArrayList<>();
    private java.lang.ref.WeakReference<MainActivity> registration;

    @Override public void onCreate(Bundle state){
        super.onCreate(state);vault=new Vault(this);updates=new AppUpdates(this,io);
        if(state!=null){accessProfile=state.getString("accessProfile","");accessURL=state.getString("accessURL","");accessVersion=state.getLong("accessVersion");if(!accessProfile.isEmpty())accessInProgress=true;}
        registration=new java.lang.ref.WeakReference<>(this);activities.add(registration);
        getOnBackPressedDispatcher().addCallback(this,new androidx.activity.OnBackPressedCallback(true){@Override public void handleOnBackPressed(){MainActivity.this.handleBack();}});
        String selected=state==null?getIntent().getStringExtra("profile"):state.getString("profile");
        if(selected!=null&&!selected.isEmpty()){profileID=selected;openApplication(state==null?getIntent().getStringExtra("route"):state.getString("route"));}else servers();
        updates.onOpen();
    }
    @Override public void onSaveInstanceState(Bundle state){state.putString("accessProfile",accessProfile);state.putString("accessURL",accessURL);state.putLong("accessVersion",accessVersion);state.putString("profile",profileID);if(web!=null&&web.getUrl()!=null){String fragment=Uri.parse(web.getUrl()).getEncodedFragment();state.putString("route",fragment==null?"":"#"+fragment);}super.onSaveInstanceState(state);}

    private int dp(int value){return Math.round(value*getResources().getDisplayMetrics().density);}
    private TextView text(String value,int size){TextView view=new TextView(this);view.setText(value);view.setTextSize(size);view.setTextColor(Color.rgb(230,234,242));view.setPadding(0,dp(6),0,dp(6));return view;}
    private Button button(String title,Runnable action){Button button=new Button(this);button.setText(title);button.setAllCaps(false);button.setOnClickListener(view->action.run());return button;}
    private void inset(android.view.View root){
        root.setOnApplyWindowInsetsListener((view,insets)->{
            if(Build.VERSION.SDK_INT>=30){android.graphics.Insets bars=insets.getInsets(android.view.WindowInsets.Type.systemBars()|android.view.WindowInsets.Type.displayCutout());android.graphics.Insets ime=insets.getInsets(android.view.WindowInsets.Type.ime());view.setPadding(bars.left,bars.top,bars.right,Math.max(bars.bottom,ime.bottom));}
            else view.setPadding(insets.getSystemWindowInsetLeft(),insets.getSystemWindowInsetTop(),insets.getSystemWindowInsetRight(),insets.getSystemWindowInsetBottom());
            return insets;
        });root.requestApplyInsets();
    }
    private void showError(Exception error){runOnUiThread(()->{if(!destroyed)new AlertDialog.Builder(this).setTitle("Connection needs attention").setMessage(NativeBridge.publicMessage(error)).setPositiveButton("OK",null).show();});}
    private void invalidateProfile(String id){
        if(MonitorService.monitoring(id))stopService(new Intent(this,MonitorService.class));
        for(java.lang.ref.WeakReference<MainActivity> entry:activities){
            MainActivity activity=entry.get();
            if(activity==null){activities.remove(entry);continue;}
            if(activity!=this&&!activity.destroyed&&id.equals(activity.profileID))activity.servers();
        }
    }
    @Override public void credentialChanged(String id){invalidateProfile(id);}
    @Override public void accessRequired(String id){
        if(destroyed||isFinishing()||accessInProgress||accessDialog!=null||!accessPrompted.add(id))return;
        accessDialog=new AlertDialog.Builder(this).setTitle("Sign in to Cloudflare Access").setMessage("This server requires Cloudflare Access. Enter your email and verification code on its sign-in page. PrAImate pairing stays separate. Server tasks keep running; failed commands are not repeated.").setNegativeButton("Later",null).setPositiveButton("Sign in",(d,w)->beginAccess(id)).create();
        accessDialog.setOnDismissListener(d->accessDialog=null);accessDialog.show();
    }
    private void beginAccess(String id){
        if(accessInProgress){Toast.makeText(this,"Finish the current Cloudflare sign-in first",Toast.LENGTH_SHORT).show();return;}
        try{
            JSONObject p=vault.profile(id);accessURL=p.getString("url");if(!accessURL.startsWith("https://"))throw new IllegalArgumentException("Cloudflare Access requires an HTTPS server address");
            accessProfile=id;accessVersion=p.optLong("accessVersion");accessInProgress=true;
            startActivityForResult(new Intent(this,CloudflareAccessActivity.class).putExtra("origin",accessURL),ACCESS_LOGIN);
        }catch(Exception error){accessProfile="";accessInProgress=false;showError(error);}
    }
    private void finishAccess(Intent data){
        String selected=accessProfile,expectedURL=accessURL;long expectedVersion=accessVersion;accessProfile="";accessInProgress=false;
        if(selected.isEmpty()||data==null)return;
        io.execute(()->{
            HttpURLConnection connection=null;
            try{
                String cookies=AccessSession.cookies(data.getStringExtra("cookies")),userAgent=data.getStringExtra("userAgent");
                if(cookies.isEmpty()||userAgent==null||userAgent.length()>2048||userAgent.contains("\r")||userAgent.contains("\n"))throw new IllegalArgumentException("Cloudflare sign-in did not return a valid session");
                connection=new ServerConnection(vault).open(selected,"/v1/discovery","GET",false,expectedURL);
                connection.setRequestProperty("Cookie",cookies);connection.setRequestProperty("User-Agent",userAgent);
                if(ServerConnection.responseCode(connection)!=200)throw new IllegalStateException("Cloudflare did not authorize this server. Check your Access policy.");
                JSONObject discovery=new JSONObject(NativeBridge.readText(connection.getInputStream(),65536));
                if(!discovery.optString("product").equals("PrAImate Server")||discovery.optInt("protocol")!=2)throw new IllegalArgumentException("The protected address is not a compatible PrAImate Server");
                vault.finishAccess(selected,expectedURL,expectedVersion,cookies,userAgent);
                runOnUiThread(()->{if(destroyed)return;accessPrompted.remove(selected);invalidateProfile(selected);if(web!=null&&profileID.equals(selected)){web.evaluateJavascript("window.dispatchEvent(new Event('praimate:native-resume'))",null);Toast.makeText(this,"Cloudflare sign-in complete. Retry the interrupted action if needed.",Toast.LENGTH_LONG).show();}else probe(selected);});
            }catch(Exception error){showError(error);}finally{if(connection!=null)connection.disconnect();}
        });
    }

    private void servers(){
        probeGeneration++;closeWeb();profileID="";
        JSONArray profiles=new JSONArray();String profileError="";
        try{profiles=vault.profiles();}catch(Exception error){profileError="Encrypted profiles could not be opened. Restore access to this device's Keystore before reconnecting.";}
        ServerHomeView home=new ServerHomeView(this,profiles,profileError,BuildConfig.VERSION_NAME,updates.automatic(),new ServerHomeView.Actions(){
            public void connect(String id){probe(id);}
            public void options(String id){profileOptions(id);}
            public void save(String name,String address)throws Exception{String normalized=Endpoint.normalize(address,BuildConfig.DEBUG);probe(vault.add(name,normalized));}
            public void checkUpdates(){updates.check(false);}
            public void automaticUpdates(boolean enabled){updates.automatic(enabled);}
        });
        setContentView(home);inset(home);
    }

    private void probe(String id){
        accessPrompted.remove(id);
        final int generation=++probeGeneration;
        Toast.makeText(this,"Connecting…",Toast.LENGTH_SHORT).show();
        io.execute(()->{
            HttpURLConnection connection=null;
            try{
                connection=new ServerConnection(vault).open(id,"/v1/discovery","GET",false);
                if(ServerConnection.responseCode(connection)!=200)throw new IllegalArgumentException("The address is not a PrAImate Server endpoint");
                JSONObject discovery=new JSONObject(NativeBridge.readText(connection.getInputStream(),65536));
                if(!discovery.optString("product").equals("PrAImate Server")||discovery.optInt("protocol")!=2)throw new IllegalArgumentException("This APK requires PrAImate Server protocol 2");
                String route=vault.profile(id).optString("route");
                runOnUiThread(()->{if(!destroyed&&generation==probeGeneration){profileID=id;openApplication(route);}});
            }catch(Exception error){runOnUiThread(()->{if(!destroyed&&generation==probeGeneration){if(error instanceof AccessSession.Required)accessRequired(id);else new AlertDialog.Builder(this).setTitle("Connection needs attention").setMessage(NativeBridge.publicMessage(error)).setNegativeButton("Close",null).setPositiveButton("Server options",(dialog,button)->profileOptions(id)).show();}});}
            finally{if(connection!=null)connection.disconnect();}
        });
    }

    private void profileOptions(String id){
        try{
            JSONObject profile=vault.profile(id);
            new AlertDialog.Builder(this).setTitle(profile.getString("name")).setItems(new String[]{"Connect","Import CA / server certificate","Remove certificate exception","Forget local pairing","Remove server","Edit server","Sign in to Cloudflare Access","Forget Cloudflare Access sign-in"},(dialog,which)->{
                try{
                    if(which==6){beginAccess(id);return;}
                    if(which==7){vault.clearAccess(id);invalidateProfile(id);Toast.makeText(this,"Cloudflare Access sign-in forgotten on this device",Toast.LENGTH_SHORT).show();return;}
                    if(which==5){editProfile(id);return;}
                    if(which==0)probe(id);
                    else if(which==1){certificateProfile=id;Intent picker=new Intent(Intent.ACTION_OPEN_DOCUMENT).setType("*/*").addCategory(Intent.CATEGORY_OPENABLE);startActivityForResult(picker,PICK_CA);}
                    else if(which==2){vault.set(id,"certificates","");invalidateProfile(id);Toast.makeText(this,"Certificate exception removed",Toast.LENGTH_SHORT).show();}
                    else new AlertDialog.Builder(this).setTitle(which==4?"Remove server?":"Forget this pairing?").setMessage("Server tasks will keep running. Revoke this device from the server administrator panel if you also want to invalidate its token there.").setNegativeButton("Cancel",null).setPositiveButton("Forget",(confirm,button)->{try{if(which==4)vault.remove(id);else vault.set(id,"token","");invalidateProfile(id);servers();}catch(Exception error){showError(error);}}).show();
                }catch(Exception error){showError(error);}
            }).show();
        }catch(Exception error){showError(error);}
    }

    private void editProfile(String id)throws Exception{
        JSONObject profile=vault.profile(id);
        LinearLayout form=new LinearLayout(this);form.setOrientation(LinearLayout.VERTICAL);form.setPadding(dp(20),0,dp(20),0);
        EditText name=new EditText(this);name.setSingleLine(true);name.setHint("Server name");name.setText(profile.getString("name"));form.addView(name);
        EditText address=new EditText(this);address.setSingleLine(true);address.setHint("HTTPS server origin");address.setInputType(android.text.InputType.TYPE_CLASS_TEXT|android.text.InputType.TYPE_TEXT_VARIATION_URI);address.setText(profile.getString("url"));form.addView(address);
        ScrollView fields=new ScrollView(this);fields.addView(form);
        AlertDialog dialog=new AlertDialog.Builder(this).setTitle("Edit server").setMessage("Changing the address clears its pairing, certificate exception and last view. Pair with the new server before using it.").setView(fields).setNegativeButton("Cancel",null).setPositiveButton("Save",null).create();
        dialog.setOnShowListener(unused->dialog.getButton(AlertDialog.BUTTON_POSITIVE).setOnClickListener(view->{
            try{boolean changed=vault.update(id,name.getText().toString(),address.getText().toString());if(changed)invalidateProfile(id);dialog.dismiss();servers();}catch(Exception error){address.setError(NativeBridge.publicMessage(error));}
        }));dialog.show();
    }

    private void openApplication(String route){
        try{activeServerURL=vault.profile(profileID).getString("url");Endpoint.route(route);}catch(Exception error){showError(error);servers();return;}
        if(!WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)){new AlertDialog.Builder(this).setTitle("Update Android System WebView").setMessage("A current Android System WebView is required for PrAImate's secure native connection.").setPositiveButton("OK",(dialog,button)->servers()).show();return;}
        closeWeb();
        web=new WebView(this);web.setBackgroundColor(Color.rgb(23,25,29));WebView.setWebContentsDebuggingEnabled(BuildConfig.DEBUG);
        WebSettings settings=web.getSettings();settings.setJavaScriptEnabled(true);settings.setDomStorageEnabled(true);settings.setAllowFileAccess(false);settings.setAllowContentAccess(false);settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);
        settings.setJavaScriptCanOpenWindowsAutomatically(false);settings.setMediaPlaybackRequiresUserGesture(true);settings.setSupportZoom(false);
        WebViewAssetLoader assets=new WebViewAssetLoader.Builder().addPathHandler("/app/",new WebViewAssetLoader.AssetsPathHandler(this)).build();
        web.setWebViewClient(new WebViewClient(){
            @Override public void onPageStarted(WebView view,String url,android.graphics.Bitmap icon){if(view==web&&bridge!=null)bridge.cancelRequests();}
            @Override public WebResourceResponse shouldInterceptRequest(WebView view,WebResourceRequest request){
                WebResourceResponse result=assets.shouldInterceptRequest(request.getUrl());
                if(result!=null){HashMap<String,String> headers=new HashMap<>();headers.put("Content-Security-Policy","default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; media-src 'self' blob:; connect-src 'self'; object-src 'none'; frame-src 'none'; base-uri 'none'");headers.put("X-Content-Type-Options","nosniff");result.setResponseHeaders(headers);return result;}
                return new WebResourceResponse("text/plain","UTF-8",403,"Blocked",Collections.emptyMap(),new java.io.ByteArrayInputStream(new byte[0]));
            }
            @Override public boolean shouldOverrideUrlLoading(WebView view,WebResourceRequest request){
                Uri uri=request.getUrl();if(Endpoint.APP_ORIGIN.equals(uri.getScheme()+"://"+uri.getHost())&&uri.getPath().equals("/app/web/index.html"))return false;
                if(request.isForMainFrame()&&(uri.getScheme().equals("https")||uri.getScheme().equals("http")))try{startActivity(new Intent(Intent.ACTION_VIEW,uri));}catch(RuntimeException ignored){}
                return true;
            }
            @Override public boolean onRenderProcessGone(WebView view,android.webkit.RenderProcessGoneDetail detail){
                String route=view.getUrl()==null?"":"#"+Uri.parse(view.getUrl()).getEncodedFragment();
                closeWeb();new AlertDialog.Builder(MainActivity.this).setTitle("Reopen workspace").setMessage("Android closed the web renderer. Your server tasks and saved conversations are retained.").setNegativeButton("Servers",(dialog,button)->servers()).setPositiveButton("Reopen",(dialog,button)->openApplication(route.equals("#null")?"":route)).show();return true;
            }
        });
        web.setWebChromeClient(new WebChromeClient(){
            @Override public boolean onShowFileChooser(WebView view,android.webkit.ValueCallback<Uri[]> callback,FileChooserParams parameters){
                if(pickerReply!=null||browserPicker!=null){callback.onReceiveValue(null);return true;}
                browserPicker=callback;
                Intent picker=new Intent(Intent.ACTION_OPEN_DOCUMENT).setType("*/*").addCategory(Intent.CATEGORY_OPENABLE).putExtra(Intent.EXTRA_ALLOW_MULTIPLE,parameters.getMode()==FileChooserParams.MODE_OPEN_MULTIPLE);
                try{startActivityForResult(picker,PICK_BROWSER);}catch(RuntimeException error){browserPicker=null;callback.onReceiveValue(null);}
                return true;
            }
            @Override public void onPermissionRequest(PermissionRequest request){
                runOnUiThread(()->{
                    if(!Endpoint.appOrigin(request.getOrigin().toString())||!java.util.Arrays.asList(request.getResources()).contains(PermissionRequest.RESOURCE_AUDIO_CAPTURE)){request.deny();return;}
                    audioPermission=request;
                    if(checkSelfPermission(Manifest.permission.RECORD_AUDIO)==PackageManager.PERMISSION_GRANTED){request.grant(new String[]{PermissionRequest.RESOURCE_AUDIO_CAPTURE});audioPermission=null;}
                    else requestPermissions(new String[]{Manifest.permission.RECORD_AUDIO},MICROPHONE);
                });
            }
            @Override public void onPermissionRequestCanceled(PermissionRequest request){if(audioPermission==request)audioPermission=null;}
        });
        bridge=new NativeBridge(vault,profileID,activeServerURL,this);if(WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER))WebViewCompat.addWebMessageListener(web,"PraimateNative",Collections.singleton(Endpoint.APP_ORIGIN),bridge);
        LinearLayout root=new LinearLayout(this);root.setBackgroundColor(Color.rgb(23,25,29));root.setOrientation(LinearLayout.VERTICAL);root.addView(web,new LinearLayout.LayoutParams(-1,-1));setContentView(root);inset(root);
        web.loadUrl(HOME+(route==null?"":route));
    }

    @Override public void action(String action,JSONObject args,NativeBridge.Reply reply)throws Exception{
        if(!java.util.Arrays.asList("navigation.back","window.close","servers.configure").contains(action)&&!vault.profile(profileID).getString("url").equals(activeServerURL))throw new IllegalStateException("Server address changed. Reopen this server before continuing.");
        switch(action){
            case "status":{JSONObject profile=vault.profile(profileID);reply.result(new JSONObject().put("serverName",profile.getString("name")).put("serverURL",profile.getString("url")).put("paired",!profile.optString("token").isEmpty()).put("monitoring",MonitorService.monitoring(profileID)).put("platform","android").put("version",BuildConfig.VERSION_NAME));break;}
            case "credentials.set":{String token=args.getString("token");if(token.length()<32||token.length()>2048)throw new IllegalArgumentException("Invalid device credential");vault.setCredential(profileID,activeServerURL,token);invalidateProfile(profileID);reply.result(true);break;}
            case "navigation.remember":{vault.setForServer(profileID,activeServerURL,"route",Endpoint.route(args.optString("route")));reply.result(true);break;}
            case "credentials.clear":vault.setCredential(profileID,activeServerURL,"");invalidateProfile(profileID);if(bridge!=null)bridge.cancelRequests();reply.result(true);break;
            case "access.login":reply.result(true);beginAccess(profileID);break;
            case "updates.check":reply.result(true);updates.check(false);break;
            case "clipboard.write":{
                String value=args.optString("text");if(value.length()>1024*1024)throw new IllegalArgumentException("Clipboard text exceeds limit");
                ClipData clip=ClipData.newPlainText("PrAImate",value);
                if(Build.VERSION.SDK_INT>=33){android.os.PersistableBundle extras=new android.os.PersistableBundle();extras.putBoolean("android.content.extra.IS_SENSITIVE",true);clip.getDescription().setExtras(extras);}
                getSystemService(android.content.ClipboardManager.class).setPrimaryClip(clip);reply.result(true);break;
            }
            case "servers.configure":reply.result(true);servers();break;
            case "window.close":reply.result(true);finish();break;
            case "navigation.back":reply.result(true);navigateBack();break;
            case "navigate":{String route=Endpoint.route(args.optString("route"));startActivity(new Intent(this,MainActivity.class).putExtra("profile",profileID).putExtra("route",route));reply.result(true);break;}
            case "files.upload":{
                requirePicker(reply);Intent picker=new Intent(Intent.ACTION_OPEN_DOCUMENT).setType("*/*").addCategory(Intent.CATEGORY_OPENABLE).putExtra(Intent.EXTRA_ALLOW_MULTIPLE,args.optBoolean("multiple"));startActivityForResult(picker,PICK_UPLOAD);break;
            }
            case "files.download":{
                requirePicker(reply);download=args;Intent picker=new Intent(Intent.ACTION_CREATE_DOCUMENT).setType("application/octet-stream").addCategory(Intent.CATEGORY_OPENABLE).putExtra(Intent.EXTRA_TITLE,args.optString("name","praimate-export"));startActivityForResult(picker,PICK_DOWNLOAD);break;
            }
            case "monitor":{
                if(!args.optBoolean("enabled")){stopService(new Intent(this,MonitorService.class));reply.result(true);}
                else if(Build.VERSION.SDK_INT>=33&&checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)!=PackageManager.PERMISSION_GRANTED){monitorReply=reply;requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS},NOTIFICATIONS);}
                else {startMonitor();reply.result(true);}break;
            }
            default:throw new IllegalArgumentException("Unknown native action");
        }
    }
    private void requirePicker(NativeBridge.Reply reply){if(pickerReply!=null||browserPicker!=null)throw new IllegalStateException("Finish the current file selection first");pickerReply=reply;}
    private void startMonitor(){startForegroundService(new Intent(this,MonitorService.class).putExtra("profile",profileID));}

    @Override public void onRequestPermissionsResult(int code,String[] permissions,int[] grants){
        super.onRequestPermissionsResult(code,permissions,grants);boolean granted=grants.length>0&&grants[0]==PackageManager.PERMISSION_GRANTED;
        if(code==MICROPHONE&&audioPermission!=null){if(granted)audioPermission.grant(new String[]{PermissionRequest.RESOURCE_AUDIO_CAPTURE});else audioPermission.deny();audioPermission=null;}
        if(code==NOTIFICATIONS&&monitorReply!=null){if(granted){startMonitor();monitorReply.result(true);}else monitorReply.error("Notifications are disabled. Server tasks still run without background monitoring.");monitorReply=null;}
    }

    @Override protected void onActivityResult(int request,int result,Intent data){
        super.onActivityResult(request,result,data);
        if(request==AppUpdates.INSTALL_PERMISSION){if(getPackageManager().canRequestPackageInstalls())updates.install();return;}
        if(request==ACCESS_LOGIN){if(result==RESULT_OK)finishAccess(data);else{accessProfile="";accessInProgress=false;}return;}
        if(request==PICK_BROWSER){android.webkit.ValueCallback<Uri[]> callback=browserPicker;browserPicker=null;if(callback!=null)callback.onReceiveValue(WebChromeClient.FileChooserParams.parseResult(result,data));return;}
        if(request==PICK_CA){if(result==RESULT_OK&&data!=null&&data.getData()!=null)importCertificates(data.getData());return;}
        NativeBridge.Reply reply=pickerReply;pickerReply=null;if(reply==null)return;
        if(result!=RESULT_OK||data==null){reply.result(request==PICK_UPLOAD?new JSONArray():"");return;}
        if(request==PICK_UPLOAD){
            List<Uri> files=new ArrayList<>();ClipData clips=data.getClipData();if(clips!=null)for(int i=0;i<clips.getItemCount();i++)files.add(clips.getItemAt(i).getUri());else if(data.getData()!=null)files.add(data.getData());
            if(files.size()>32){reply.error("Select at most 32 files at a time");return;}
            String selected=profileID,expectedURL=activeServerURL;io.execute(()->uploadFiles(selected,expectedURL,files,reply));
        }else if(request==PICK_DOWNLOAD&&data.getData()!=null){String selected=profileID,expectedURL=activeServerURL;JSONObject descriptor=download;Uri destination=data.getData();io.execute(()->downloadFile(selected,expectedURL,descriptor,destination,reply));}
    }

    private void uploadFiles(String selected,String expectedURL,List<Uri> files,NativeBridge.Reply reply){
        JSONArray uploaded=new JSONArray();
        try{for(Uri file:files){
            String name="attachment";long size=-1;
            try(Cursor cursor=getContentResolver().query(file,new String[]{OpenableColumns.DISPLAY_NAME,OpenableColumns.SIZE},null,null,null)){if(cursor!=null&&cursor.moveToFirst()){name=cursor.getString(0);if(!cursor.isNull(1))size=cursor.getLong(1);}}
            if(size>FILE_LIMIT)throw new IllegalArgumentException("File exceeds the 512 MiB upload limit");
            HttpURLConnection connection=new ServerConnection(vault).open(selected,"/v1/upload?name="+URLEncoder.encode(name,"UTF-8"),"PUT",true,expectedURL);
            try{connection.setDoOutput(true);connection.setReadTimeout(600000);connection.setChunkedStreamingMode(32768);connection.setRequestProperty("Content-Type","application/octet-stream");
                try(InputStream input=getContentResolver().openInputStream(file);OutputStream output=connection.getOutputStream()){copy(input,output,FILE_LIMIT);}
                int code=ServerConnection.responseCode(connection);if(code!=200)throw new IllegalStateException("Upload failed (HTTP "+code+")");
                uploaded.put(new JSONObject(NativeBridge.readText(connection.getInputStream(),65536)));
            }finally{connection.disconnect();}
        }reply.result(uploaded);}catch(Exception error){reply.error(NativeBridge.publicMessage(error));if(error instanceof AccessSession.Required)runOnUiThread(()->accessRequired(selected));}
    }
    private void downloadFile(String selected,String expectedURL,JSONObject descriptor,Uri destination,NativeBridge.Reply reply){
        HttpURLConnection connection=null;
        try{
            String path="/v1/download?workspace="+URLEncoder.encode(descriptor.getString("workspace"),"UTF-8")+"&path="+URLEncoder.encode(descriptor.getString("path"),"UTF-8");
            connection=new ServerConnection(vault).open(selected,path,"GET",true,expectedURL);connection.setReadTimeout(600000);
            int code=ServerConnection.responseCode(connection);if(code!=200)throw new IllegalStateException("Download failed (HTTP "+code+")");
            try(InputStream input=connection.getInputStream();OutputStream output=getContentResolver().openOutputStream(destination,"w")){copy(input,output,Long.MAX_VALUE);}
            reply.result(descriptor.optString("name","Saved file"));
        }catch(Exception error){try{android.provider.DocumentsContract.deleteDocument(getContentResolver(),destination);}catch(Exception ignored){}reply.error(NativeBridge.publicMessage(error));if(error instanceof AccessSession.Required)runOnUiThread(()->accessRequired(selected));}
        finally{if(connection!=null)connection.disconnect();}
    }
    private static void copy(InputStream input,OutputStream output,long limit)throws Exception{
        if(input==null||output==null)throw new IllegalArgumentException("The selected file is unavailable");byte[] buffer=new byte[32768];int count;long total=0;
        while((count=input.read(buffer))!=-1){total+=count;if(total>limit)throw new IllegalArgumentException("File exceeds transfer limit");output.write(buffer,0,count);}
    }

    private void importCertificates(Uri file){
        final String selected=certificateProfile;
        io.execute(()->{
            try{
                byte[] raw;
                try(InputStream input=getContentResolver().openInputStream(file);java.io.ByteArrayOutputStream output=new java.io.ByteArrayOutputStream()){copy(input,output,128*1024);raw=output.toByteArray();}
                java.util.Collection<? extends java.security.cert.Certificate> certificates=CertificateFactory.getInstance("X.509").generateCertificates(new java.io.ByteArrayInputStream(raw));
                if(certificates.isEmpty()||certificates.size()>10)throw new IllegalArgumentException("Select a PEM/DER certificate or CA bundle with at most 10 certificates");
                StringBuilder pem=new StringBuilder(),details=new StringBuilder();
                for(java.security.cert.Certificate item:certificates){X509Certificate cert=(X509Certificate)item;cert.checkValidity();byte[] bytes=cert.getEncoded();pem.append("-----BEGIN CERTIFICATE-----\n").append(android.util.Base64.encodeToString(bytes,android.util.Base64.NO_WRAP)).append("\n-----END CERTIFICATE-----\n");details.append(cert.getSubjectX500Principal().getName()).append("\nSHA-256: ");for(byte value:MessageDigest.getInstance("SHA-256").digest(bytes))details.append(String.format(java.util.Locale.ROOT,"%02X",value));details.append("\n\n");}
                runOnUiThread(()->{if(!destroyed)new AlertDialog.Builder(this).setTitle("Trust certificates for this server?").setMessage(details+"This exception applies only to this saved server. Hostname and expiry validation remain enabled.").setNegativeButton("Cancel",null).setPositiveButton("Trust",(dialog,button)->{try{vault.set(selected,"certificates",pem.toString());invalidateProfile(selected);probe(selected);}catch(Exception error){showError(error);}}).show();});
            }catch(Exception error){showError(error);}
        });
    }
    private void navigateBack(){if(web!=null&&web.canGoBack())web.goBack();else if(web!=null&&isTaskRoot())servers();else finish();}
    private void handleBack(){if(web==null){navigateBack();return;}web.evaluateJavascript("Boolean(window.praimateMobile && window.praimateMobile.onBack && window.praimateMobile.onBack())",result->{if(!"true".equals(result))navigateBack();});}
    private void rememberCurrentRoute(){
        if(web==null||web.getUrl()==null||profileID.isEmpty())return;
        String selected=profileID,expectedURL=activeServerURL,fragment=Uri.parse(web.getUrl()).getEncodedFragment(),route=fragment==null?"":"#"+fragment;
        io.execute(()->{try{vault.setForServer(selected,expectedURL,"route",Endpoint.route(route));}catch(Exception ignored){}});
    }
    @Override protected void onPause(){rememberCurrentRoute();if(web!=null)web.onPause();super.onPause();}
    @Override protected void onResume(){super.onResume();if(web!=null){web.onResume();web.evaluateJavascript("window.dispatchEvent(new Event('praimate:native-resume'))",null);}}
    private void closeWeb(){rememberCurrentRoute();if(bridge!=null){bridge.close();bridge=null;}if(web!=null){web.stopLoading();if(web.getParent() instanceof android.view.ViewGroup)((android.view.ViewGroup)web.getParent()).removeView(web);web.destroy();web=null;}if(audioPermission!=null){audioPermission.deny();audioPermission=null;}if(browserPicker!=null){browserPicker.onReceiveValue(null);browserPicker=null;}}
    @Override protected void onDestroy(){destroyed=true;if(updates!=null)updates.close();if(accessDialog!=null)accessDialog.dismiss();if(isFinishing()&&!accessProfile.isEmpty())accessInProgress=false;activities.remove(registration);probeGeneration++;closeWeb();io.shutdown();super.onDestroy();}
}
