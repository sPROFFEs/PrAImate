package com.praimate.mobile;

import java.net.URI;
import java.util.LinkedHashMap;
import java.util.Locale;
import java.util.Map;

/** Pure parsing policy shared by the login surface and native HTTP transport. */
public final class AccessSession {
    private AccessSession(){}
    public static final class Required extends IllegalStateException {
        public Required(){super("Cloudflare Access sign-in is required. Sign in with your email and verification code, then reconnect. Server tasks are retained.");}
    }
    public static String cookies(String raw){
        if(raw==null||raw.isEmpty())return "";
        if(raw.length()>32768||raw.indexOf('\r')>=0||raw.indexOf('\n')>=0)throw new IllegalArgumentException("Invalid Cloudflare Access session");
        Map<String,String> result=new LinkedHashMap<>();
        for(String item:raw.split(";")){
            int split=item.indexOf('=');if(split<=0)continue;
            String name=item.substring(0,split).trim(),value=item.substring(split+1).trim();
            if(!name.equals("CF_Authorization")&&!name.equals("CF_Binding")&&!name.equals("CF_AppSession"))continue;
            for(int i=0;i<value.length();i++)if(value.charAt(i)<0x21||value.charAt(i)>0x7e||value.charAt(i)=='"'||value.charAt(i)=='\\')throw new IllegalArgumentException("Invalid Cloudflare Access session");
            if(result.put(name,value)!=null)throw new IllegalArgumentException("Ambiguous Cloudflare Access session");
        }
        if(!result.containsKey("CF_Authorization")||result.get("CF_Authorization").isEmpty())return "";
        StringBuilder header=new StringBuilder();for(Map.Entry<String,String> item:result.entrySet()){if(header.length()>0)header.append("; ");header.append(item.getKey()).append('=').append(item.getValue());}return header.toString();
    }
    public static boolean challenge(int status,String location,String contentType,boolean cloudflare){
        if(status>=300&&status<400&&location!=null){
            try{URI uri=URI.create(location);String host=uri.getHost();String path=uri.getPath();if((host!=null&&host.toLowerCase(Locale.ROOT).endsWith(".cloudflareaccess.com"))||(path!=null&&path.startsWith("/cdn-cgi/access/")))return true;}catch(IllegalArgumentException ignored){}
        }
        return cloudflare&&contentType!=null&&contentType.toLowerCase(Locale.ROOT).contains("text/html")&&(status==200||status==401||status==403||status==503);
    }
}
