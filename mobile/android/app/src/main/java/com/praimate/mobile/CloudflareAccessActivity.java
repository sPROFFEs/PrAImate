package com.praimate.mobile;

import android.app.Activity;
import android.app.AlertDialog;
import android.content.Intent;
import android.graphics.Color;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.webkit.CookieManager;
import android.webkit.WebResourceRequest;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;
import android.widget.Button;
import android.widget.LinearLayout;
import android.widget.TextView;

/** Isolated login process: no native bridge, bearer, files, microphone or Vault.
 * Only Cloudflare application cookies return through the private Activity result.
 */
public final class CloudflareAccessActivity extends Activity {
    private WebView web;
    private String origin;
    private boolean returned;
    private static boolean directoryConfigured;
    private static java.lang.ref.WeakReference<CloudflareAccessActivity> activeLogin=new java.lang.ref.WeakReference<>(null);
    private static final String LOGIN_PATH="/v1/status";
    @Override public void onCreate(Bundle state){
        super.onCreate(state);setResult(RESULT_CANCELED);
        CloudflareAccessActivity previous=activeLogin.get();if(previous!=null&&!previous.isFinishing()&&!previous.isDestroyed()){finish();return;}activeLogin=new java.lang.ref.WeakReference<>(this);
        if(Build.VERSION.SDK_INT<28){new AlertDialog.Builder(this).setTitle("Android update required").setMessage("Cloudflare Access sign-in requires Android 9 or later for an isolated browser session.").setPositiveButton("Close",(d,w)->finish()).show();return;}
        try{origin=Endpoint.normalize(getIntent().getStringExtra("origin"),false);if(!origin.startsWith("https://"))throw new IllegalArgumentException();}catch(Exception invalid){finish();return;}
        // This activity is the only WebView in :cloudflare_access. Its directory
        // and cookies are separate from all packaged application WebViews.
        if(!directoryConfigured){WebView.setDataDirectorySuffix("cloudflare-access");directoryConfigured=true;}
        LinearLayout root=new LinearLayout(this);root.setOrientation(LinearLayout.VERTICAL);root.setBackgroundColor(Color.rgb(23,25,29));
        TextView address=new TextView(this);address.setTextColor(Color.WHITE);address.setPadding(20,16,20,16);address.setText(getString(R.string.access_address,origin));root.addView(address);
        Button cancel=new Button(this);cancel.setText(R.string.access_cancel);cancel.setAllCaps(false);cancel.setOnClickListener(v->finish());root.addView(cancel);
        web=new WebView(this);WebSettings settings=web.getSettings();settings.setJavaScriptEnabled(true);settings.setDomStorageEnabled(true);settings.setAllowFileAccess(false);settings.setAllowContentAccess(false);settings.setMixedContentMode(WebSettings.MIXED_CONTENT_NEVER_ALLOW);settings.setJavaScriptCanOpenWindowsAutomatically(false);settings.setSupportMultipleWindows(false);settings.setSaveFormData(false);
        web.setWebViewClient(new WebViewClient(){
            @Override public boolean shouldOverrideUrlLoading(WebView view,WebResourceRequest request){return !"https".equals(request.getUrl().getScheme());}
            @Override public void onPageStarted(WebView view,String url,android.graphics.Bitmap favicon){Uri uri=Uri.parse(url);address.setText(getString(R.string.access_address,uri.getHost()));}
            @Override public void onPageFinished(WebView view,String url){
                if(returned||isFinishing())return;
                try{
                    Uri current=Uri.parse(url);
                    if(!Endpoint.sameHTTPSOrigin(origin,url)||!LOGIN_PATH.equals(current.getPath()))return;
                    String cookies=AccessSession.cookies(CookieManager.getInstance().getCookie(origin+LOGIN_PATH));
                    if(cookies.isEmpty()){returned=true;new AlertDialog.Builder(CloudflareAccessActivity.this).setTitle("Cloudflare Access").setMessage(R.string.access_not_required).setPositiveButton("Close",(d,w)->finish()).setOnCancelListener(d->finish()).show();return;}
                    returned=true;setResult(RESULT_OK,new Intent().putExtra("cookies",cookies).putExtra("userAgent",settings.getUserAgentString()));finish();
                }catch(IllegalArgumentException ignored){}
            }
            // The default SSL handler cancels: no certificate bypass on login.
        });
        root.addView(web,new LinearLayout.LayoutParams(-1,0,1));setContentView(root);
        root.setOnApplyWindowInsetsListener((v,insets)->{if(Build.VERSION.SDK_INT>=30){android.graphics.Insets bars=insets.getInsets(android.view.WindowInsets.Type.systemBars()|android.view.WindowInsets.Type.displayCutout());android.graphics.Insets ime=insets.getInsets(android.view.WindowInsets.Type.ime());v.setPadding(bars.left,bars.top,bars.right,Math.max(bars.bottom,ime.bottom));}else v.setPadding(insets.getSystemWindowInsetLeft(),insets.getSystemWindowInsetTop(),insets.getSystemWindowInsetRight(),insets.getSystemWindowInsetBottom());return insets;});root.requestApplyInsets();
        CookieManager cookies=CookieManager.getInstance();cookies.setAcceptCookie(true);cookies.setAcceptThirdPartyCookies(web,false);
        // Always start clean; a crashed or cancelled login cannot select another
        // profile's Access account. No existing application cookie is imported.
        android.webkit.WebStorage.getInstance().deleteAllData();
        cookies.removeAllCookies(done->{cookies.flush();if(web!=null&&!isFinishing()){web.clearCache(true);web.loadUrl(origin+LOGIN_PATH);}});
    }
    @Override protected void onDestroy(){
        if(web!=null){web.stopLoading();((android.view.ViewGroup)web.getParent()).removeView(web);web.destroy();web=null;android.webkit.WebStorage.getInstance().deleteAllData();CookieManager.getInstance().removeAllCookies(done->CookieManager.getInstance().flush());}
        if(activeLogin.get()==this)activeLogin.clear();
        super.onDestroy();
    }
}
