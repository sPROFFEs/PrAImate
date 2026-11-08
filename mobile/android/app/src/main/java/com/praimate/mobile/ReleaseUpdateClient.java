package com.praimate.mobile;

import org.json.JSONArray;
import org.json.JSONObject;
import java.io.*;
import java.net.HttpURLConnection;
import java.net.URI;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.util.concurrent.atomic.AtomicBoolean;

/** Public HTTPS only: never shares the server transport, cookies or bearer. */
public final class ReleaseUpdateClient {
    public static final long APK_LIMIT=64L*1024*1024;
    public static final class Info {
        public final String tag,version,asset,url,sha256;
        public final long size;
        Info(String tag,String asset,String sha256,long size){this(tag,tag,asset,sha256,size);}
        Info(String tag,String version,String asset,String sha256,long size){this.tag=tag;this.version=version;this.asset=asset;this.url=UpdatePolicy.releaseURL(tag,asset);this.sha256=sha256;this.size=size;}
        public JSONObject json()throws Exception{return new JSONObject().put("tag",tag).put("version",version).put("asset",asset).put("sha256",sha256).put("size",size);}
        public static Info fromJSON(JSONObject value)throws Exception{return new Info(value.getString("tag"),value.optString("version",value.getString("tag")),value.getString("asset"),value.getString("sha256"),value.getLong("size"));}
    }
    public interface Progress {void update(long bytes,long total);}
    private final String installed,asset;
    private final boolean preview;
    private volatile HttpURLConnection downloadConnection;
    public ReleaseUpdateClient(String installed,boolean preview){this.installed=installed;this.preview=preview;asset=preview?"praimate-mobile-preview.apk":"praimate-mobile.apk";}
    public Info select(String json)throws Exception{
        JSONArray releases=new JSONArray(json);Info best=null;
        for(int i=0;i<releases.length();i++){
            JSONObject r=releases.getJSONObject(i);String tag=r.optString("tag_name");
            if(r.optBoolean("draft")||(!preview&&r.optBoolean("prerelease")))continue;
            String version;
            try{version=UpdatePolicy.androidVersion(tag,r.optString("body"));if(!UpdatePolicy.newer(version,installed))continue;}catch(IllegalArgumentException invalid){continue;}
            JSONArray assets=r.optJSONArray("assets");if(assets==null)continue;
            for(int j=0;j<assets.length();j++){
                JSONObject a=assets.getJSONObject(j);if(!asset.equals(a.optString("name")))continue;
                long size=a.optLong("size",-1);if(size<=0||size>APK_LIMIT)continue;
                String digest=a.optString("digest","");digest=digest.matches("sha256:[a-fA-F0-9]{64}")?digest.substring(7).toLowerCase(java.util.Locale.ROOT):"";
                Info candidate=new Info(tag,version,asset,digest,size);if(best==null||UpdatePolicy.newer(version,best.version))best=candidate;
            }
        }
        return best;
    }
    public Info check()throws Exception{
        Info info;
        try{info=select(read(UpdatePolicy.API,8*1024*1024));}
        catch(Exception apiUnavailable){
            // One HTTPS fallback, without HTML parsing or aggressive retries.
            String tag=latestWebTag(),metadata="";
            try{metadata=read(UpdatePolicy.releaseURL(tag,"ANDROID-VERSION"),4096);}catch(FileNotFoundException legacyRelease){/* Older releases use the tag version. */}
            String version=UpdatePolicy.androidVersion(tag,metadata);if(!UpdatePolicy.newer(version,installed))return null;
            String digest=UpdatePolicy.checksum(read(UpdatePolicy.releaseURL(tag,"SHA256SUMS"),1024*1024),asset);
            return new Info(tag,version,asset,digest,-1);
        }
        if(info!=null&&info.sha256.isEmpty())info=new Info(info.tag,info.version,asset,UpdatePolicy.checksum(read(UpdatePolicy.releaseURL(info.tag,"SHA256SUMS"),1024*1024),asset),info.size);
        return info;
    }
    private HttpURLConnection request(String address,String method,boolean redirects)throws Exception{
        for(int i=0;i<8;i++){
            UpdatePolicy.trustedURL(address);HttpURLConnection c=(HttpURLConnection)new URL(address).openConnection();
            c.setInstanceFollowRedirects(false);c.setConnectTimeout(15000);c.setReadTimeout(30000);c.setRequestMethod(method);c.setRequestProperty("User-Agent","PrAImate-Android/"+installed);c.setRequestProperty("Accept","application/vnd.github+json");
            int status=c.getResponseCode();
            if(redirects&&status>=300&&status<400){String next=c.getHeaderField("Location");c.disconnect();if(next==null)throw new IOException("GitHub returned an invalid update redirect");address=new URL(new URL(address),next).toString();continue;}
            return c;
        }
        throw new IOException("Too many GitHub update redirects");
    }
    private String latestWebTag()throws Exception{
        HttpURLConnection c=request(UpdatePolicy.WEB+"/releases/latest","HEAD",false);
        try{
            int code=c.getResponseCode();String location=c.getHeaderField("Location");if(code<300||code>=400||location==null)throw new IOException("GitHub update checks are unavailable. Try again later or open Releases.");
            String next=new URL(new URL(UpdatePolicy.WEB),location).toString();UpdatePolicy.trustedURL(next);URI u=new URI(next);
            String prefix="/"+UpdatePolicy.REPO+"/releases/tag/";if(!u.getPath().toLowerCase(java.util.Locale.ROOT).startsWith(prefix.toLowerCase(java.util.Locale.ROOT)))throw new IOException("Unexpected GitHub release redirect");
            String tag=u.getPath().substring(prefix.length());UpdatePolicy.releaseURL(tag,asset);return tag;
        }finally{c.disconnect();}
    }
    private String read(String address,int limit)throws Exception{
        HttpURLConnection c=request(address,"GET",true);
        try{if(c.getResponseCode()==404)throw new FileNotFoundException("GitHub update metadata was not found");if(c.getResponseCode()!=200)throw new IOException("GitHub update request returned HTTP "+c.getResponseCode());
            try(InputStream input=c.getInputStream();ByteArrayOutputStream output=new ByteArrayOutputStream()){byte[] buffer=new byte[8192];int n;while((n=input.read(buffer))!=-1){if(output.size()+n>limit)throw new IOException("GitHub update metadata exceeds the limit");output.write(buffer,0,n);}return output.toString("UTF-8");}
        }finally{c.disconnect();}
    }
    public void cancel(){HttpURLConnection c=downloadConnection;if(c!=null)c.disconnect();}
    public void download(Info info,File target,AtomicBoolean cancelled,Progress progress)throws Exception{
        if(!info.asset.equals(asset)||!info.sha256.matches("[a-fA-F0-9]{64}"))throw new IOException("The update has no verified checksum");
        File partial=new File(target.getParentFile(),"download.part");
        if(!target.getParentFile().isDirectory()&&!target.getParentFile().mkdirs())throw new IOException("Cannot create private update cache");
        HttpURLConnection c=request(info.url,"GET",true);downloadConnection=c;
        try{
            if(c.getResponseCode()!=200)throw new IOException("APK download returned HTTP "+c.getResponseCode());
            long expected=c.getContentLengthLong();if(expected>APK_LIMIT)throw new IOException("APK exceeds the download limit");long total=0;MessageDigest digest=MessageDigest.getInstance("SHA-256");
            try(InputStream input=c.getInputStream();OutputStream output=new FileOutputStream(partial)){
                byte[] buffer=new byte[32768];int n;while((n=input.read(buffer))!=-1){if(cancelled.get())throw new IOException("Update download cancelled");total+=n;if(total>APK_LIMIT)throw new IOException("APK exceeds the download limit");output.write(buffer,0,n);digest.update(buffer,0,n);progress.update(total,expected);}
            }
            if(cancelled.get()||(info.size>0&&info.size!=total)||!hex(digest.digest()).equalsIgnoreCase(info.sha256))throw new IOException("APK checksum or size verification failed");
            java.nio.file.Files.move(partial.toPath(),target.toPath(),java.nio.file.StandardCopyOption.REPLACE_EXISTING);
        }finally{downloadConnection=null;c.disconnect();if(partial.exists())partial.delete();}
    }
    public static String hash(File file)throws Exception{MessageDigest digest=MessageDigest.getInstance("SHA-256");try(InputStream input=new FileInputStream(file)){byte[] b=new byte[32768];int n;while((n=input.read(b))!=-1)digest.update(b,0,n);}return hex(digest.digest());}
    private static String hex(byte[] bytes){StringBuilder text=new StringBuilder();for(byte b:bytes)text.append(String.format(java.util.Locale.ROOT,"%02x",b&255));return text.toString();}
}
